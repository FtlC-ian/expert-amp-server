// Package capture records passive observations of the amplifier display.
package capture

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	ampRuntime "github.com/FtlC-ian/expert-amp-server/internal/runtime"
)

const captureFormat = "expert-amp-display-capture/v1"

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"buildDate,omitempty"`
	Channel   string `json:"channel,omitempty"`
}

type ServerMetadata struct {
	BaseURL string `json:"baseUrl"`
	VersionInfo
}

type AmplifierMetadata struct {
	ModelName string        `json:"modelName,omitempty"`
	Telemetry api.Telemetry `json:"telemetry"`
}

// Record is one first-seen exact LCD state. Chars and Attrs retain the full
// protocol geometry, including custom glyph bytes that have no decoded text.
type Record struct {
	Format            string                           `json:"format"`
	StateHash         string                           `json:"stateHash"`
	PreviousStateHash string                           `json:"previousStateHash,omitempty"`
	TransitionLabel   string                           `json:"transitionLabel,omitempty"`
	CapturedAt        time.Time                        `json:"capturedAt"`
	SnapshotUpdatedAt time.Time                        `json:"snapshotUpdatedAt,omitempty"`
	Sequence          uint64                           `json:"sequence"`
	Source            string                           `json:"source,omitempty"`
	FrameKind         string                           `json:"frameKind,omitempty"`
	Chars             [display.Rows][display.Cols]byte `json:"chars"`
	Attrs             [display.Rows][display.Cols]byte `json:"attrs"`
	ScreenText        string                           `json:"screenText"`
	LCDFlags          *api.LCDFlags                    `json:"lcdFlags,omitempty"`
	Frame             api.FrameInfo                    `json:"frame"`
	Server            ServerMetadata                   `json:"server"`
	Amplifier         AmplifierMetadata                `json:"amplifier"`
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (c Client) Version(ctx context.Context) (VersionInfo, error) {
	var version VersionInfo
	if err := c.get(ctx, "/api/v1/version", &version); err != nil {
		return VersionInfo{}, err
	}
	return version, nil
}

func (c Client) Snapshot(ctx context.Context) (ampRuntime.Snapshot, error) {
	var snapshot ampRuntime.Snapshot
	if err := c.get(ctx, "/api/v1/runtime/snapshot", &snapshot); err != nil {
		return ampRuntime.Snapshot{}, err
	}
	return snapshot, nil
}

func (c Client) get(ctx context.Context, path string, dst any) error {
	baseURL, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return fmt.Errorf("base URL must use http or https")
	}
	requestURL := baseURL.JoinPath(strings.TrimLeft(path, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create GET %s: %w", path, err)
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: server returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode GET %s: %w", path, err)
	}
	if !envelope.Success {
		message := envelope.Error
		if message == "" {
			message = envelope.Message
		}
		return fmt.Errorf("GET %s: API failure: %s", path, message)
	}
	if err := json.Unmarshal(envelope.Data, dst); err != nil {
		return fmt.Errorf("decode GET %s data: %w", path, err)
	}
	return nil
}

type Options struct {
	Client          Client
	OutputPath      string
	PollInterval    time.Duration
	MaxUnique       int
	Once            bool
	TransitionLabel string
	Now             func() time.Time
	OnRecord        func(Record)
}

type Stats struct {
	Polls   int
	Written int
	Skipped int
}

// Run polls read-only endpoints and appends first-seen states as NDJSON.
func Run(ctx context.Context, opts Options) (Stats, error) {
	if strings.TrimSpace(opts.OutputPath) == "" {
		return Stats{}, errors.New("output path is required")
	}
	if opts.PollInterval <= 0 {
		return Stats{}, errors.New("poll interval must be positive")
	}
	if opts.MaxUnique < 0 {
		return Stats{}, errors.New("max unique must not be negative")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	version, err := opts.Client.Version(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("read server metadata: %w", err)
	}
	output, seen, err := openOutput(opts.OutputPath)
	if err != nil {
		return Stats{}, err
	}
	defer output.Close()
	encoder := json.NewEncoder(output)
	transitionLabel := strings.TrimSpace(opts.TransitionLabel)
	var previousHash string

	var stats Stats
	for {
		snapshot, err := opts.Client.Snapshot(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return stats, nil
			}
			return stats, err
		}
		stats.Polls++
		hash := StateHash(snapshot.State, snapshot.Frame.LCDFlags)
		if _, exists := seen[hash]; exists {
			stats.Skipped++
			// A previously captured state can still be the real source of the
			// next transition after a recorder restart or menu revisit.
			previousHash = hash
		} else {
			record := Record{
				Format:            captureFormat,
				StateHash:         hash,
				PreviousStateHash: previousHash,
				TransitionLabel:   transitionLabel,
				CapturedAt:        opts.Now().UTC(),
				SnapshotUpdatedAt: snapshot.UpdatedAt,
				Sequence:          snapshot.Sequence,
				Source:            snapshot.Source,
				FrameKind:         snapshot.FrameKind,
				Chars:             snapshot.State.Chars,
				Attrs:             snapshot.State.Attrs,
				ScreenText:        snapshot.Frame.ScreenText,
				LCDFlags:          snapshot.Frame.LCDFlags,
				Frame:             snapshot.Frame,
				Server: ServerMetadata{
					BaseURL:     strings.TrimRight(opts.Client.BaseURL, "/"),
					VersionInfo: version,
				},
				Amplifier: AmplifierMetadata{
					ModelName: snapshot.Telemetry.ModelName,
					Telemetry: snapshot.Telemetry,
				},
			}
			if err := encoder.Encode(record); err != nil {
				return stats, fmt.Errorf("append capture: %w", err)
			}
			if err := output.Sync(); err != nil {
				return stats, fmt.Errorf("sync capture: %w", err)
			}
			seen[hash] = struct{}{}
			previousHash = hash
			transitionLabel = ""
			stats.Written++
			if opts.OnRecord != nil {
				opts.OnRecord(record)
			}
		}

		if opts.Once || (opts.MaxUnique > 0 && stats.Written >= opts.MaxUnique) {
			return stats, nil
		}
		timer := time.NewTimer(opts.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return stats, nil
		case <-timer.C:
		}
	}
}

// StateHash returns a deterministic identity for the exact character,
// attribute, and LCD flag state. Sequence and timestamps intentionally do not
// participate, so repeated snapshots deduplicate across recorder restarts.
func StateHash(state display.State, flags *api.LCDFlags) string {
	h := sha256.New()
	_, _ = h.Write([]byte(captureFormat))
	for row := range state.Chars {
		_, _ = h.Write(state.Chars[row][:])
	}
	for row := range state.Attrs {
		_, _ = h.Write(state.Attrs[row][:])
	}
	if flags == nil {
		_, _ = h.Write([]byte{0})
	} else {
		_, _ = h.Write([]byte{1})
		var buf [4]byte
		binary.LittleEndian.PutUint16(buf[0:2], flags.RawInverted)
		binary.LittleEndian.PutUint16(buf[2:4], flags.Decoded)
		_, _ = h.Write(buf[:])
		_, _ = h.Write([]byte{boolByte(flags.ChecksumPresent), boolByte(flags.ChecksumValid)})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func openOutput(path string) (*os.File, map[string]struct{}, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open capture output: %w", err)
	}
	seen := make(map[string]struct{})
	needsSeparator := false
	if info, err := file.Stat(); err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("stat capture output: %w", err)
	} else if info.Size() > 0 {
		var last [1]byte
		if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
			file.Close()
			return nil, nil, fmt.Errorf("read capture output boundary: %w", err)
		}
		needsSeparator = last[0] != '\n'
	}
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var existing struct {
			StateHash string `json:"stateHash"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &existing); err != nil {
			file.Close()
			return nil, nil, fmt.Errorf("read existing capture line %d: %w", line, err)
		}
		if existing.StateHash == "" {
			file.Close()
			return nil, nil, fmt.Errorf("read existing capture line %d: missing stateHash", line)
		}
		seen[existing.StateHash] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("read existing capture: %w", err)
	}
	if needsSeparator {
		if _, err := file.WriteString("\n"); err != nil {
			file.Close()
			return nil, nil, fmt.Errorf("separate existing capture: %w", err)
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return nil, nil, fmt.Errorf("sync capture separator: %w", err)
		}
	}
	return file, seen, nil
}
