package capture

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	ampRuntime "github.com/FtlC-ian/expert-amp-server/internal/runtime"
)

func TestClientPreservesBaseURLPathPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/expert-amp/api/v1/version" {
			t.Errorf("request path = %q, want path prefix preserved", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(api.Response{Success: true, Data: VersionInfo{Version: "1.2.3"}})
	}))
	defer server.Close()

	version, err := (Client{BaseURL: server.URL + "/expert-amp/", HTTPClient: server.Client()}).Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version.Version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", version.Version)
	}
}

func TestClientPreservesEscapedBaseURLPathPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != "/team%2Falpha/api/v1/version" {
			t.Errorf("request URI = %q, want escaped path prefix preserved", r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(api.Response{Success: true, Data: VersionInfo{Version: "1.2.3"}})
	}))
	defer server.Close()

	if _, err := (Client{BaseURL: server.URL + "/team%2Falpha", HTTPClient: server.Client()}).Version(context.Background()); err != nil {
		t.Fatalf("Version: %v", err)
	}
}

func TestRunRecordsUniqueStatesUsingGETOnly(t *testing.T) {
	first := testSnapshot(4, "HOME", 0x07fe)
	second := testSnapshot(5, "SET MENU", 0x27fe)
	server := snapshotServer(t, []ampRuntime.Snapshot{first, first, second})
	defer server.Close()

	output := filepath.Join(t.TempDir(), "capture.ndjson")
	stats, err := Run(context.Background(), Options{
		Client:          Client{BaseURL: server.URL, HTTPClient: server.Client()},
		OutputPath:      output,
		PollInterval:    time.Millisecond,
		MaxUnique:       2,
		TransitionLabel: "press SET",
		Now:             func() time.Time { return time.Date(2026, 7, 20, 14, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Polls != 3 || stats.Written != 2 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, want 3 polls, 2 written, 1 skipped", stats)
	}

	records := readRecords(t, output)
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].Format != captureFormat || records[0].Sequence != 4 {
		t.Fatalf("unexpected first record: %+v", records[0])
	}
	if records[0].Chars != first.State.Chars || records[0].Attrs != first.State.Attrs {
		t.Fatal("first record did not preserve exact chars and attrs")
	}
	if records[0].ScreenText != "HOME" || records[0].LCDFlags == nil || records[0].LCDFlags.Decoded != 0x07fe {
		t.Fatalf("first frame metadata = %+v", records[0].Frame)
	}
	if records[0].Server.Version != "1.2.3" || records[0].Amplifier.ModelName != "Expert 2K-FA" {
		t.Fatalf("capture metadata = server %+v amplifier %+v", records[0].Server, records[0].Amplifier)
	}
	if records[0].TransitionLabel != "press SET" {
		t.Fatalf("transition label = %q", records[0].TransitionLabel)
	}
	if records[1].TransitionLabel != "" {
		t.Fatalf("transition label leaked to later state: %q", records[1].TransitionLabel)
	}
	if records[1].PreviousStateHash != records[0].StateHash {
		t.Fatalf("previous state hash = %q, want %q", records[1].PreviousStateHash, records[0].StateHash)
	}
}

func TestRunResumesDedupFromExistingOutput(t *testing.T) {
	first := testSnapshot(4, "HOME", 0x07fe)
	second := testSnapshot(5, "SET MENU", 0x27fe)
	output := filepath.Join(t.TempDir(), "capture.ndjson")
	existing := Record{Format: captureFormat, StateHash: StateHash(first.State, first.Frame.LCDFlags), Sequence: first.Sequence}
	file, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(existing); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	server := snapshotServer(t, []ampRuntime.Snapshot{first, second})
	defer server.Close()
	stats, err := Run(context.Background(), Options{
		Client:       Client{BaseURL: server.URL, HTTPClient: server.Client()},
		OutputPath:   output,
		PollInterval: time.Millisecond,
		MaxUnique:    1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Polls != 2 || stats.Written != 1 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, want resumed duplicate to be skipped", stats)
	}
	records := readRecords(t, output)
	if len(records) != 2 || records[1].Sequence != second.Sequence {
		t.Fatalf("records after resume = %+v", records)
	}
	if records[1].PreviousStateHash != existing.StateHash {
		t.Fatalf("previous state hash = %q, want existing hash %q", records[1].PreviousStateHash, existing.StateHash)
	}
}

func TestRunUsesLastObservedDuplicateAsTransitionSource(t *testing.T) {
	first := testSnapshot(4, "HOME", 0x07fe)
	second := testSnapshot(5, "SET MENU", 0x27fe)
	output := filepath.Join(t.TempDir(), "capture.ndjson")
	for _, snapshot := range []ampRuntime.Snapshot{first, second} {
		existing := Record{
			Format:    captureFormat,
			StateHash: StateHash(snapshot.State, snapshot.Frame.LCDFlags),
			Sequence:  snapshot.Sequence,
		}
		file, err := os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(file).Encode(existing); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}

	third := testSnapshot(6, "FAN MODE", 0x27fe)
	server := snapshotServer(t, []ampRuntime.Snapshot{first, third})
	defer server.Close()
	stats, err := Run(context.Background(), Options{
		Client:       Client{BaseURL: server.URL, HTTPClient: server.Client()},
		OutputPath:   output,
		PollInterval: time.Millisecond,
		MaxUnique:    1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Polls != 2 || stats.Written != 1 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	records := readRecords(t, output)
	if got, want := records[2].PreviousStateHash, StateHash(first.State, first.Frame.LCDFlags); got != want {
		t.Fatalf("previous state hash = %q, want last observed duplicate %q", got, want)
	}
}

func TestRunDoesNotInferTransitionFromPreviousSession(t *testing.T) {
	old := testSnapshot(4, "OLD SESSION", 0x07fe)
	current := testSnapshot(5, "CURRENT", 0x27fe)
	output := filepath.Join(t.TempDir(), "capture.ndjson")
	existing := Record{Format: captureFormat, StateHash: StateHash(old.State, old.Frame.LCDFlags), Sequence: old.Sequence}
	file, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(existing); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	server := snapshotServer(t, []ampRuntime.Snapshot{current})
	defer server.Close()
	stats, err := Run(context.Background(), Options{
		Client:       Client{BaseURL: server.URL, HTTPClient: server.Client()},
		OutputPath:   output,
		PollInterval: time.Millisecond,
		Once:         true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Polls != 1 || stats.Written != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	records := readRecords(t, output)
	if got := records[1].PreviousStateHash; got != "" {
		t.Fatalf("previous state hash = %q, want empty without a current-session predecessor", got)
	}
}

func TestRunSeparatesExistingFinalRecordWithoutNewline(t *testing.T) {
	first := testSnapshot(4, "HOME", 0x07fe)
	second := testSnapshot(5, "SET MENU", 0x27fe)
	output := filepath.Join(t.TempDir(), "capture.ndjson")
	existing := Record{Format: captureFormat, StateHash: StateHash(first.State, first.Frame.LCDFlags), Sequence: first.Sequence}
	data, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, data, 0o600); err != nil {
		t.Fatal(err)
	}

	server := snapshotServer(t, []ampRuntime.Snapshot{second})
	defer server.Close()
	stats, err := Run(context.Background(), Options{
		Client:       Client{BaseURL: server.URL, HTTPClient: server.Client()},
		OutputPath:   output,
		PollInterval: time.Millisecond,
		Once:         true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Polls != 1 || stats.Written != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	records := readRecords(t, output)
	if len(records) != 2 || records[0].StateHash != existing.StateHash || records[1].Sequence != second.Sequence {
		t.Fatalf("records = %+v", records)
	}
}

func TestStateHashCoversExactDisplayAndFlagsButNotSnapshotMetadata(t *testing.T) {
	state := display.NewState()
	flags := &api.LCDFlags{RawInverted: 0xf801, Decoded: 0x07fe, ChecksumPresent: true, ChecksumValid: true}
	want := StateHash(state, flags)
	copyFlags := *flags
	if got := StateHash(state, &copyFlags); got != want {
		t.Fatalf("equal state hash = %q, want %q", got, want)
	}
	changedChar := state
	changedChar.Chars[7][39]++
	if got := StateHash(changedChar, flags); got == want {
		t.Fatal("character change did not change hash")
	}
	changedAttr := state
	changedAttr.Attrs[7][39]++
	if got := StateHash(changedAttr, flags); got == want {
		t.Fatal("attribute change did not change hash")
	}
	changedFlags := *flags
	changedFlags.Decoded = 0x27fe
	if got := StateHash(state, &changedFlags); got == want {
		t.Fatal("LCD flag change did not change hash")
	}
}

func testSnapshot(sequence uint64, text string, decoded uint16) ampRuntime.Snapshot {
	state := display.NewState()
	state.SetRow(0, text)
	state.SetAttr(0, 0, 0x80)
	flags := &api.LCDFlags{
		RawInverted:     ^decoded,
		Decoded:         decoded,
		ChecksumPresent: true,
		ChecksumValid:   true,
		LEDs:            &api.LCDLEDs{Set: decoded&0x2000 != 0},
	}
	return ampRuntime.Snapshot{
		State:     state,
		Telemetry: api.Telemetry{ModelName: "Expert 2K-FA", Band: "20m", Source: "serial"},
		Frame: api.FrameInfo{
			Source:      "/dev/ttyUSB0",
			Length:      371,
			StartOffset: 9,
			ScreenText:  text,
			LCDFlags:    flags,
		},
		FrameKind: "serial",
		Source:    "serial",
		Sequence:  sequence,
		UpdatedAt: time.Date(2026, 7, 20, 13, 59, int(sequence), 0, time.UTC),
	}
}

func snapshotServer(t *testing.T, snapshots []ampRuntime.Snapshot) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	index := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/version":
			_ = json.NewEncoder(w).Encode(api.Response{Success: true, Data: VersionInfo{Version: "1.2.3", Commit: "abc123"}})
		case "/api/v1/runtime/snapshot":
			mu.Lock()
			if index >= len(snapshots) {
				mu.Unlock()
				t.Error("received more snapshot requests than expected")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			snapshot := snapshots[index]
			index++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(api.Response{Success: true, Data: snapshot})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func readRecords(t *testing.T, path string) []Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	records := make([]Record, 0, len(lines))
	for _, line := range lines {
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		records = append(records, record)
	}
	return records
}
