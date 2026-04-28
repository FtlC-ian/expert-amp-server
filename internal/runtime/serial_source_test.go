package runtime

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
)

// mockSerialPort implements serial.Port for testing.
type mockSerialPort struct {
	mu            sync.Mutex
	chunks        [][]byte
	chunkIdx      int
	closed        bool
	written       [][]byte
	blockWrite    chan struct{}
	setReadTimout []time.Duration
	writeStarted  chan struct{}
}

func (m *mockSerialPort) Read(buf []byte) (int, error) {
	if m.chunkIdx >= len(m.chunks) {
		time.Sleep(10 * time.Millisecond)
		return 0, nil
	}
	chunk := m.chunks[m.chunkIdx]
	m.chunkIdx++
	n := copy(buf, chunk)
	return n, nil
}

func (m *mockSerialPort) Write(buf []byte) (int, error) {
	if m.writeStarted != nil {
		select {
		case m.writeStarted <- struct{}{}:
		default:
		}
	}
	if m.blockWrite != nil {
		<-m.blockWrite
	}
	m.mu.Lock()
	m.written = append(m.written, append([]byte(nil), buf...))
	m.mu.Unlock()
	return len(buf), nil
}
func (m *mockSerialPort) writtenSnapshot() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.written))
	for i := range m.written {
		out[i] = append([]byte(nil), m.written[i]...)
	}
	return out
}
func (m *mockSerialPort) Close() error {
	m.closed = true
	return nil
}
func (m *mockSerialPort) SetReadTimeout(d time.Duration) error {
	m.setReadTimout = append(m.setReadTimout, d)
	return nil
}
func (m *mockSerialPort) SetDTR(bool) error { return nil }
func (m *mockSerialPort) SetRTS(bool) error { return nil }

type mockSerialOpener struct {
	port *mockSerialPort
}

func (m *mockSerialOpener) Open(string, int) (serial.Port, error) {
	return m.port, nil
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("DecodeString(%q): %v", s, err)
	}
	return b
}

func TestSerialSourcePollReturnsInitialUpdate(t *testing.T) {
	cfg := SerialSourceConfig{Port: "/dev/ttyTEST0", BaudRate: 115200, ReadTimeout: 10 * time.Millisecond, ReadSize: 512, MinFrameLen: 64, MaxBuffer: 8192}

	src := NewSerialSource(cfg, &mockSerialOpener{port: &mockSerialPort{}}, Update{
		State:     display.DemoState(),
		Telemetry: api.Telemetry{Band: "20m", Source: "fixture:home", TX: boolPtr(false)},
		Frame:     api.FrameInfo{Source: "fixture:home", Length: 371},
		FrameKind: "home",
		Source:    "fixture:home",
	})

	update, err := src.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if update.Source != "fixture:home" {
		t.Fatalf("source = %q, want fixture:home", update.Source)
	}
}

func TestSerialSourceDiagnosticsInitialState(t *testing.T) {
	src := NewSerialSource(SerialSourceConfig{Port: "/dev/ttyUSB1"}, &mockSerialOpener{port: &mockSerialPort{}}, Update{
		Source: "fixture:home",
	})
	diag := src.Diagnostics()
	if diag.SerialPort != "/dev/ttyUSB1" {
		t.Fatalf("SerialPort = %q, want /dev/ttyUSB1", diag.SerialPort)
	}
	if diag.FramesSeen != 0 {
		t.Fatalf("FramesSeen = %d, want 0", diag.FramesSeen)
	}
	if diag.Connected {
		t.Fatal("should not be connected before start")
	}
}

func TestSerialSourceLiveIngestUpdatesPoll(t *testing.T) {
	// Build a complete frame from the real fixture file.
	rawFrame, err := protocol.ReadFixtureBytes("fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}

	// Construct a serial stream chunk: prefix + padding + frame body + closing prefix.
	// This simulates what the stream decoder would see from the serial port.
	portChunk := append([]byte{}, protocol.RadioDisplayPrefix...)
	// The fixture already starts with the prefix, so the stream decoder
	// will see: prefix(from fixture start)...body...prefix(from closing boundary).
	// We add a closing boundary to complete the frame extraction.
	portChunk = append(portChunk, rawFrame[len(protocol.RadioDisplayPrefix):]...)
	portChunk = append(portChunk, protocol.RadioDisplayPrefix...)

	mockPort := &mockSerialPort{
		chunks: [][]byte{portChunk},
	}

	cfg := SerialSourceConfig{
		Port:        "/dev/ttyTEST0",
		BaudRate:    115200,
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
		MinFrameLen: 64,
		MaxBuffer:   8192,
	}

	src := NewSerialSource(cfg, &mockSerialOpener{port: mockPort}, Update{
		State:     display.DemoState(),
		Telemetry: api.Telemetry{Source: "fixture:home", TX: boolPtr(false)},
		Frame:     api.FrameInfo{Source: "fixture:home"},
		FrameKind: "home",
		Source:    "fixture:home",
	})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)

	// Wait for the read loop to process the chunk.
	time.Sleep(200 * time.Millisecond)
	cancel()

	update, err := src.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if update.Source != "serial" {
		t.Fatalf("source after ingest = %q, want serial", update.Source)
	}
	if update.FrameKind != "serial" {
		t.Fatalf("frameKind = %q, want serial", update.FrameKind)
	}

	diag := src.Diagnostics()
	if diag.FramesSeen < 1 {
		t.Fatalf("FramesSeen = %d, want >= 1", diag.FramesSeen)
	}
	if diag.LastFrameLength <= 0 {
		t.Fatalf("LastFrameLength = %d, want > 0", diag.LastFrameLength)
	}

	// Verify the decoded state matches what we'd get from the fixture directly.
	fixtureState, _, err := protocol.LoadFixtureState("fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	if update.State != fixtureState {
		t.Fatal("live-decoded state does not match fixture state")
	}
}

func TestSerialSourceStopsOnCancel(t *testing.T) {
	mockPort := &mockSerialPort{chunks: [][]byte{}}
	cfg := SerialSourceConfig{Port: "/dev/ttyTEST0", ReadTimeout: 10 * time.Millisecond, ReadSize: 512, MinFrameLen: 64, MaxBuffer: 8192}

	src := NewSerialSource(cfg, &mockSerialOpener{port: mockPort}, Update{Source: "fixture:home"})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)

	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	diag := src.Diagnostics()
	if diag.Connected {
		t.Fatal("should not be connected after cancel")
	}
}

func TestSerialSourceAppliesToStore(t *testing.T) {
	// Verify that a SerialSource can be used as a runtime.Source with the
	// existing Poller and Store.
	rawFrame, err := protocol.ReadFixtureBytes("fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}

	portChunk := append([]byte{}, protocol.RadioDisplayPrefix...)
	portChunk = append(portChunk, rawFrame[len(protocol.RadioDisplayPrefix):]...)
	portChunk = append(portChunk, protocol.RadioDisplayPrefix...)

	mockPort := &mockSerialPort{
		chunks: [][]byte{portChunk},
	}

	cfg := SerialSourceConfig{
		Port:        "/dev/ttyTEST0",
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
		MinFrameLen: 64,
		MaxBuffer:   8192,
	}

	src := NewSerialSource(cfg, &mockSerialOpener{port: mockPort}, Update{
		State:     display.DemoState(),
		Telemetry: api.Telemetry{Source: "fixture:home", TX: boolPtr(false)},
		Frame:     api.FrameInfo{Source: "fixture:home"},
		FrameKind: "home",
		Source:    "fixture:home",
	})

	store := NewStore(Snapshot{
		State:     display.DemoState(),
		Telemetry: api.Telemetry{Source: "fixture:home", TX: boolPtr(false)},
		FrameKind: "home",
		Source:    "fixture:home",
		Sequence:  1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()

	// Wait for a frame to be decoded.
	time.Sleep(200 * time.Millisecond)

	// The poller should pick up the serial source update.
	poller := &Poller{Source: src, Store: store, Interval: 10 * time.Millisecond}
	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	go func() {
		_ = poller.Run(pollerCtx)
	}()
	defer pollerCancel()

	time.Sleep(100 * time.Millisecond)

	snap := store.Current()
	if snap.Source != "serial" {
		t.Fatalf("snapshot source = %q, want serial", snap.Source)
	}
	if snap.Sequence < 2 {
		t.Fatalf("snapshot sequence = %d, want >= 2 (should have been updated from serial)", snap.Sequence)
	}
}

func TestSerialSourceSendButtonUsesHeldLivePort(t *testing.T) {
	rawFrame, err := protocol.ReadFixtureBytes("fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}

	portChunk := append([]byte{}, protocol.RadioDisplayPrefix...)
	portChunk = append(portChunk, rawFrame[len(protocol.RadioDisplayPrefix):]...)
	portChunk = append(portChunk, protocol.RadioDisplayPrefix...)

	tests := []struct {
		name       string
		actionName string
		want       []byte
	}{
		{name: "input", actionName: "input", want: []byte{0x55, 0x55, 0x55, 0x01, 0x01, 0x01}},
		{name: "band-", actionName: "band-", want: []byte{0x55, 0x55, 0x55, 0x01, 0x02, 0x02}},
		{name: "band+", actionName: "band+", want: []byte{0x55, 0x55, 0x55, 0x01, 0x03, 0x03}},
		{name: "antenna", actionName: "antenna", want: []byte{0x55, 0x55, 0x55, 0x01, 0x04, 0x04}},
		{name: "l-", actionName: "l-", want: []byte{0x55, 0x55, 0x55, 0x01, 0x05, 0x05}},
		{name: "l+", actionName: "l+", want: []byte{0x55, 0x55, 0x55, 0x01, 0x06, 0x06}},
		{name: "c-", actionName: "c-", want: []byte{0x55, 0x55, 0x55, 0x01, 0x07, 0x07}},
		{name: "c+", actionName: "c+", want: []byte{0x55, 0x55, 0x55, 0x01, 0x08, 0x08}},
		{name: "tune", actionName: "tune", want: []byte{0x55, 0x55, 0x55, 0x01, 0x09, 0x09}},
		{name: "off", actionName: "off", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0a, 0x0a}},
		{name: "power", actionName: "power", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0b, 0x0b}},
		{name: "display", actionName: "display", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0c, 0x0c}},
		{name: "operate", actionName: "operate", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0d, 0x0d}},
		{name: "cat", actionName: "cat", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0e, 0x0e}},
		{name: "set", actionName: "set", want: []byte{0x55, 0x55, 0x55, 0x01, 0x11, 0x11}},
		{name: "up alias", actionName: "up", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0f, 0x0f}},
		{name: "down alias", actionName: "down", want: []byte{0x55, 0x55, 0x55, 0x01, 0x10, 0x10}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockPort := &mockSerialPort{chunks: [][]byte{portChunk}}
			src := NewSerialSource(SerialSourceConfig{
				Port:        "/dev/ttyTEST0",
				ReadTimeout: 10 * time.Millisecond,
				ReadSize:    512,
				MinFrameLen: 64,
				MaxBuffer:   8192,
			}, &mockSerialOpener{port: mockPort}, Update{Source: "fixture:home"})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			src.Start(ctx)
			time.Sleep(100 * time.Millisecond)

			result, err := src.SendButton(context.Background(), api.ButtonAction{Name: tc.actionName})
			if err != nil {
				t.Fatalf("SendButton error: %v", err)
			}
			if !result.Sent || result.Transport != "serial-live" {
				t.Fatalf("unexpected result: %+v", result)
			}
			written := mockPort.writtenSnapshot()
			if len(written) == 0 {
				t.Fatal("expected write through held live port")
			}
			got := written[len(written)-1]
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("frame[%d] = 0x%02x, want 0x%02x", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSerialSourceSendButtonUnavailableWithoutHeldPort(t *testing.T) {
	src := NewSerialSource(SerialSourceConfig{Port: "/dev/ttyTEST0"}, &mockSerialOpener{port: &mockSerialPort{}}, Update{Source: "fixture:home"})
	_, err := src.SendButton(context.Background(), api.ButtonAction{Name: "set"})
	if err == nil || err.Error() != "button transport unavailable" {
		t.Fatalf("error = %v, want button transport unavailable", err)
	}
}

func TestSerialSourceSendButtonRejectsBlockedActionWithoutAliasTrap(t *testing.T) {
	src := NewSerialSource(SerialSourceConfig{Port: "/dev/ttyTEST0"}, &mockSerialOpener{port: &mockSerialPort{}}, Update{Source: "fixture:home"})
	for _, name := range []string{"back", "on", "standby"} {
		_, err := src.SendButton(context.Background(), api.ButtonAction{Name: name})
		if err == nil || err.Error() != "unsupported button action: "+name {
			t.Fatalf("%s error = %v, want unsupported button action", name, err)
		}
	}
}

func TestSerialSourceParsesDocumentedStatusFrame(t *testing.T) {
	statusFrame, err := os.ReadFile("../protocol/testdata/status_response_example.bin")
	if err != nil {
		t.Fatalf("ReadFile status fixture: %v", err)
	}
	mockPort := &mockSerialPort{chunks: [][]byte{statusFrame}}
	src := NewSerialSource(SerialSourceConfig{
		Port:        "/dev/ttyTEST0",
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
		MinFrameLen: 64,
		MaxBuffer:   8192,
	}, &mockSerialOpener{port: mockPort}, Update{Source: "fixture:home"})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()

	status := src.StatusState().CurrentProtocolNative()
	if status.Provenance != "status-poll" {
		t.Fatalf("status provenance = %q, want status-poll", status.Provenance)
	}
	if status.ModelName != "EXPERT 2K-FA" || status.BandCode != "00" || status.BandText != "160m" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.PowerWatts == nil || *status.PowerWatts != 0 {
		t.Fatalf("powerWatts = %v, want 0", status.PowerWatts)
	}
}

func TestSerialSourceParsesLiveCapturedStatusFrame(t *testing.T) {
	statusFrame := mustDecodeHex(t, "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d2c0d0a")
	mockPort := &mockSerialPort{chunks: [][]byte{statusFrame}}
	src := NewSerialSource(SerialSourceConfig{
		Port:        "/dev/ttyTEST0",
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
		MinFrameLen: 64,
		MaxBuffer:   8192,
	}, &mockSerialOpener{port: mockPort}, Update{Source: "fixture:home"})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()

	status := src.StatusState().CurrentProtocolNative()
	if status.Provenance != "status-poll" {
		t.Fatalf("status provenance = %q, want status-poll", status.Provenance)
	}
	if status.ModelName != "EXPERT 1.3K-FA" || status.BandCode != "05" || status.BandText != "20m" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.AntennaBank != "A" || status.Input != "2" || status.Antenna != "4" {
		t.Fatalf("unexpected routing fields: %+v", status)
	}
}

func TestSerialSourceUsesSeparateDisplayAndStatusPollFrames(t *testing.T) {
	mockPort := &mockSerialPort{}
	src := NewSerialSource(SerialSourceConfig{
		Port:                      "/dev/ttyTEST0",
		ReadTimeout:               10 * time.Millisecond,
		ReadSize:                  512,
		MinFrameLen:               64,
		MaxBuffer:                 8192,
		DisplayPollEnabled:        true,
		DisplayPollInterval:       25 * time.Millisecond,
		DisplayPollFrameHex:       "555555018080",
		StatusPollCommandEnabled:  true,
		StatusPollCommandInterval: 25 * time.Millisecond,
		StatusPollCommandFrameHex: "555555019090",
	}, &mockSerialOpener{port: mockPort}, Update{Telemetry: api.Telemetry{Band: "20m"}})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	seenDisplay := false
	seenStatus := false
	for _, frame := range mockPort.writtenSnapshot() {
		hexFrame := hex.EncodeToString(frame)
		if hexFrame == "555555018080" {
			seenDisplay = true
		}
		if hexFrame == "555555019090" {
			seenStatus = true
		}
	}
	if !seenDisplay || !seenStatus {
		t.Fatalf("separate poll frames not both observed, display=%v status=%v writes=%v", seenDisplay, seenStatus, mockPort.writtenSnapshot())
	}
}

func TestSerialSourceStatusFrameDoesNotOverwriteDisplaySnapshotTelemetry(t *testing.T) {
	statusFrame := mustDecodeHex(t, "aaaaaa432c31334b2c532c522c412c322c30352c34622c30722c4c2c303030302c20302e30302c20302e30302c20302e302c20302e302c2032352c3030302c3030302c4e2c4e2c3b0d2c0d0a")
	src := NewSerialSource(SerialSourceConfig{Port: "/dev/ttyTEST0"}, &mockSerialOpener{port: &mockSerialPort{}}, Update{
		Telemetry: api.Telemetry{Band: "6m", OperatingState: "operate", Source: "serial", Provenance: "display-frame"},
		Source:    "serial",
	})

	src.applyStatusFrame(statusFrame)
	update, err := src.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if update.Telemetry.Band != "6m" || update.Telemetry.Provenance != "display-frame" {
		t.Fatalf("status frame clobbered display snapshot telemetry: %+v", update.Telemetry)
	}
	status := src.StatusState().CurrentProtocolNative()
	if status.Provenance != "status-poll" {
		t.Fatalf("status provenance = %q, want status-poll", status.Provenance)
	}
}

func TestSerialSourceWriteFrameTimeoutRestoresReadTimeout(t *testing.T) {
	blocked := make(chan struct{})
	started := make(chan struct{}, 1)
	mockPort := &mockSerialPort{blockWrite: blocked, writeStarted: started}
	src := NewSerialSource(SerialSourceConfig{Port: "/dev/ttyTEST0", ReadTimeout: 25 * time.Millisecond}, &mockSerialOpener{port: mockPort}, Update{})
	src.setPort(mockPort)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- src.writeFrame(ctx, []byte{0x55})
	}()

	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("write did not start")
	}

	err := <-errCh
	close(blocked)
	if err == nil || !contains(err.Error(), "underlying serial write may still complete asynchronously") {
		t.Fatalf("writeFrame error = %v, want timeout note", err)
	}
	if len(mockPort.setReadTimout) < 2 {
		t.Fatalf("SetReadTimeout calls = %v, want deadline then restore", mockPort.setReadTimout)
	}
	if got := mockPort.setReadTimout[len(mockPort.setReadTimout)-1]; got != 25*time.Millisecond {
		t.Fatalf("final read timeout = %s, want restore to 25ms", got)
	}
}

func contains(s, substr string) bool { return strings.Contains(s, substr) }

func TestSerialPollSchedulerModes(t *testing.T) {
	start := time.Unix(100, 0)
	tests := []struct {
		name  string
		mode  string
		steps []struct {
			at   time.Duration
			want string
		}
	}{
		{name: "status", mode: "status", steps: []struct {
			at   time.Duration
			want string
		}{{0, "status"}, {50 * time.Millisecond, ""}, {100 * time.Millisecond, "status"}}},
		{name: "display", mode: "display", steps: []struct {
			at   time.Duration
			want string
		}{{0, "display"}, {50 * time.Millisecond, ""}, {100 * time.Millisecond, "display"}}},
		{name: "off", mode: "off", steps: []struct {
			at   time.Duration
			want string
		}{{0, ""}, {100 * time.Millisecond, ""}}},
		{name: "both", mode: "both", steps: []struct {
			at   time.Duration
			want string
		}{{0, "status"}, {49 * time.Millisecond, ""}, {50 * time.Millisecond, "display"}, {100 * time.Millisecond, "status"}, {150 * time.Millisecond, "display"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSerialPollScheduler(start, tc.mode, 100*time.Millisecond, true, true)
			for _, step := range tc.steps {
				if got := s.nextDue(start.Add(step.at), tc.mode); got != step.want {
					t.Fatalf("nextDue(%s) = %q, want %q", step.at, got, step.want)
				}
			}
		})
	}
}

func TestSerialPollSchedulerGracefullyDegradesWhenPollFrameUnavailable(t *testing.T) {
	start := time.Unix(100, 0)
	tests := []struct {
		name       string
		hasDisplay bool
		hasStatus  bool
		want       string
	}{
		{name: "status only available", hasDisplay: false, hasStatus: true, want: "status"},
		{name: "display only available", hasDisplay: true, hasStatus: false, want: "display"},
		{name: "neither available", hasDisplay: false, hasStatus: false, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSerialPollScheduler(start, "both", 100*time.Millisecond, tc.hasDisplay, tc.hasStatus)
			if got := s.nextDue(start, "both"); got != tc.want {
				t.Fatalf("nextDue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSerialPollSchedulerResetsWhenModeChanges(t *testing.T) {
	start := time.Unix(100, 0)
	s := newSerialPollScheduler(start, "both", 100*time.Millisecond, true, true)
	if got := s.nextDue(start, "both"); got != "status" {
		t.Fatalf("initial due = %q, want status", got)
	}
	if got := s.nextDue(start.Add(10*time.Millisecond), "off"); got != "" {
		t.Fatalf("off mode due = %q, want none", got)
	}
	if got := s.nextDue(start.Add(20*time.Millisecond), "display"); got != "display" {
		t.Fatalf("display mode due = %q, want display", got)
	}
	if got := s.nextDue(start.Add(30*time.Millisecond), "status"); got != "status" {
		t.Fatalf("status mode due = %q, want status", got)
	}
}

func TestSerialSourceUnifiedBothModeAlternatesPollWrites(t *testing.T) {
	mockPort := &mockSerialPort{}
	src := NewSerialSource(SerialSourceConfig{
		Port:                      "/dev/ttyTEST0",
		ReadTimeout:               5 * time.Millisecond,
		ReadSize:                  512,
		MinFrameLen:               64,
		MaxBuffer:                 8192,
		PollingMode:               "both",
		PollInterval:              40 * time.Millisecond,
		DisplayPollEnabled:        true,
		DisplayPollFrameHex:       "555555018080",
		StatusPollCommandEnabled:  true,
		StatusPollCommandFrameHex: "555555019090",
	}, &mockSerialOpener{port: mockPort}, Update{Telemetry: api.Telemetry{Band: "20m"}})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	time.Sleep(130 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	var got []string
	written := mockPort.writtenSnapshot()
	for _, frame := range written {
		switch hex.EncodeToString(frame) {
		case "555555019090":
			got = append(got, "status")
		case "555555018080":
			got = append(got, "display")
		}
		if len(got) >= 4 {
			break
		}
	}
	want := []string{"status", "display", "status", "display"}
	if len(got) < len(want) {
		t.Fatalf("writes = %v, want at least %v; raw=%v", got, want, written)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("writes = %v, want prefix %v", got, want)
		}
	}
}

func TestSerialPollSchedulerSkipsMissedSlotsWithoutDoubleFire(t *testing.T) {
	start := time.Unix(100, 0)
	s := newSerialPollScheduler(start, "both", 100*time.Millisecond, true, true)
	if got := s.nextDue(start, "both"); got != "status" {
		t.Fatalf("first due = %q, want status", got)
	}
	if got := s.nextDue(start.Add(275*time.Millisecond), "both"); got != "display" {
		t.Fatalf("late due = %q, want single display write", got)
	}
	if got := s.nextDue(start.Add(276*time.Millisecond), "both"); got != "" {
		t.Fatalf("immediate second due = %q, want no double-fire", got)
	}
}
