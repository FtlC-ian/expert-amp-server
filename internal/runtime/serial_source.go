package runtime

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

// SerialSourceConfig holds all parameters for the live serial ingest path.
type SerialSourceConfig struct {
	Port                      string
	BaudRate                  int
	ReadTimeout               time.Duration
	ReadSize                  int
	DisplayPollEnabled        bool
	DisplayPollInterval       time.Duration
	DisplayPollFrameHex       string
	StatusPollCommandEnabled  bool
	StatusPollCommandInterval time.Duration
	StatusPollCommandFrameHex string
	AssertDTR                 bool
	AssertRTS                 bool
	MinFrameLen               int
	MaxBuffer                 int
	DisplayPollEnabledFn      func() bool
	StatusPollEnabledFn       func() bool
}

// Defaults returns a SerialSourceConfig with sensible SPE Expert defaults.
func DefaultSerialSourceConfig(port string) SerialSourceConfig {
	return SerialSourceConfig{
		Port:                      port,
		BaudRate:                  115200,
		ReadTimeout:               250 * time.Millisecond,
		ReadSize:                  512,
		DisplayPollEnabled:        true,
		DisplayPollInterval:       1 * time.Second,
		DisplayPollFrameHex:       hex.EncodeToString(protocol.DisplayPollCommand),
		StatusPollCommandEnabled:  true,
		StatusPollCommandInterval: 500 * time.Millisecond,
		StatusPollCommandFrameHex: hex.EncodeToString(protocol.StatusPollCommand),
		AssertDTR:                 true,
		AssertRTS:                 true,
		MinFrameLen:               64,
		MaxBuffer:                 8192,
	}
}

// IngestDiagnostics holds counters and timestamps for the live serial path.
type IngestDiagnostics struct {
	FramesSeen      int64  `json:"framesSeen"`
	DecodeErrors    int64  `json:"decodeErrors"`
	LastFrameLength int64  `json:"lastFrameLength"`
	LastFrameAt     string `json:"lastFrameAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	SerialPort      string `json:"serialPort"`
	Connected       bool   `json:"connected"`
}

// SerialSource implements [Source] by reading from a real serial port,
// extracting display frames using [protocol.DisplayStreamDecoder], and
// decoding them into runtime Updates.
//
// It handles reconnection internally with a simple backoff. Callers
// (the Poller) call Poll to get the latest decoded state.
type SerialSource struct {
	cfg    SerialSourceConfig
	opener serial.PortOpener

	mu          sync.RWMutex
	latest      Update
	statusState *StatusState
	diag        IngestDiagnostics
	running     bool

	framesSeen   atomic.Int64
	decodeErrors atomic.Int64
	lastFrameLen atomic.Int64
	lastFrameAt  atomic.Int64
	connected    atomic.Bool

	lastErrMu sync.RWMutex
	lastErr   string

	portMu sync.RWMutex
	port   serial.Port

	writeMu sync.Mutex
	specs   map[string]transport.ButtonSpec

	// cancel stops the background read loop.
	cancel context.CancelFunc
}

// NewSerialSource creates a SerialSource. Call [SerialSource.Start] to begin
// the background read loop; call [SerialSource.Stop] to shut it down.
func NewSerialSource(cfg SerialSourceConfig, opener serial.PortOpener, initial Update) *SerialSource {
	return &SerialSource{
		cfg:         cfg,
		opener:      opener,
		latest:      initial,
		statusState: NewStatusState(api.Status{Telemetry: initial.Telemetry}),
		specs:       transport.DefaultButtonMap(),
	}
}

// Start begins the background serial read loop. It returns immediately; the
// loop runs until [SerialSource.Stop] is called or a fatal error occurs.
func (s *SerialSource) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.readLoop(ctx)
}

// Stop signals the background read loop to exit and waits for it.
func (s *SerialSource) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Poll returns the most recently decoded display frame as a runtime Update.
// This satisfies the [Source] interface so the existing Poller can use
// SerialSource as a drop-in replacement for FixtureSource.
func (s *SerialSource) Poll(_ context.Context) (Update, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest, nil
}

func (s *SerialSource) StatusState() *StatusState {
	if s == nil {
		return nil
	}
	return s.statusState
}

// Diagnostics returns the current ingest diagnostics snapshot.
func (s *SerialSource) Diagnostics() IngestDiagnostics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d := s.diag
	d.FramesSeen = s.framesSeen.Load()
	d.DecodeErrors = s.decodeErrors.Load()
	d.LastFrameLength = s.lastFrameLen.Load()
	d.Connected = s.connected.Load()
	d.SerialPort = s.cfg.Port
	if ts := s.lastFrameAt.Load(); ts > 0 {
		d.LastFrameAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	s.lastErrMu.RLock()
	d.LastError = s.lastErr
	s.lastErrMu.RUnlock()
	return d
}

// SendButton writes a safe action frame through the currently held live serial port.
func (s *SerialSource) SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error) {
	action = action.Normalized()
	spec, ok := s.specs[action.Name]
	if !ok || !spec.Safe || spec.Code == nil {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial-live"}, transport.InvalidButtonActionError(action.Name)
	}
	frame := []byte{0x55, 0x55, 0x55, 0x01, *spec.Code, *spec.Code}
	if err := s.writeFrame(ctx, frame); err != nil {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial-live", FrameHex: hex.EncodeToString(frame)}, err
	}
	return api.ActionResult{Name: action.Name, Queued: false, Sent: true, Transport: "serial-live", FrameHex: hex.EncodeToString(frame)}, nil
}

func (s *SerialSource) SendWake(ctx context.Context) (api.ActionResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if strings.TrimSpace(s.cfg.Port) == "" {
		return api.ActionResult{Name: "wake", Queued: false, Sent: false, Transport: "serial-live-wake"}, transport.WakeTransportUnavailableError()
	}
	if port := s.currentPort(); port != nil {
		_ = port.Close()
		s.setPort(nil)
	}
	opener := s.opener
	if opener == nil {
		opener = serial.OpenRealPort{}
	}
	wake := transport.NewLocalWakeTransport(s.cfg.Port, opener, timeoutForContext(ctx))
	result, err := wake.SendWake(ctx)
	result.Transport = "serial-live-wake"
	return result, err
}

func (s *SerialSource) readLoop(ctx context.Context) {
	s.connected.Store(false)

	decoder := protocol.NewDisplayStreamDecoder(protocol.StreamDecoderConfig{
		MinFrameLen: s.cfg.MinFrameLen,
		MaxBuffer:   s.cfg.MaxBuffer,
	})
	statusDecoder := protocol.NewStatusStreamDecoder()

	serialCfg := serial.Config{
		Port:        s.cfg.Port,
		BaudRate:    s.cfg.BaudRate,
		ReadTimeout: s.cfg.ReadTimeout,
		ReadSize:    s.cfg.ReadSize,
		AssertDTR:   s.cfg.AssertDTR,
		AssertRTS:   s.cfg.AssertRTS,
	}
	displayPollFrame, displayPollErr := s.decodePollFrame("display", s.cfg.DisplayPollEnabled, s.cfg.DisplayPollFrameHex)
	if displayPollErr != nil {
		s.setErr(displayPollErr.Error())
		log.Printf("serial source: %s", s.lastErr)
	}
	statusPollFrame, statusPollErr := s.decodePollFrame("status", s.cfg.StatusPollCommandEnabled, s.cfg.StatusPollCommandFrameHex)
	if statusPollErr != nil {
		s.setErr(statusPollErr.Error())
		log.Printf("serial source: %s", s.lastErr)
	}

	opener := s.opener
	if opener == nil {
		opener = serial.OpenRealPort{}
	}

	for {
		if ctx.Err() != nil {
			s.setPort(nil)
			s.connected.Store(false)
			return
		}

		port, err := opener.Open(serialCfg.Port, serialCfg.BaudRate)
		if err != nil {
			err = fmt.Errorf("open serial %s: %w", serialCfg.Port, err)
		} else {
			err = s.readFromPort(ctx, port, serialCfg, decoder, statusDecoder, displayPollFrame, statusPollFrame)
		}
		if err == nil {
			// Context canceled.
			s.setPort(nil)
			s.connected.Store(false)
			return
		}
		s.setPort(nil)
		s.connected.Store(false)
		s.setErr(err.Error())
		log.Printf("serial source read error: %v", err)

		// Backoff before reconnect.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *SerialSource) readFromPort(ctx context.Context, port serial.Port, cfg serial.Config, decoder *protocol.DisplayStreamDecoder, statusDecoder *protocol.StatusStreamDecoder, displayPollFrame []byte, statusPollFrame []byte) error {
	defer port.Close()
	s.setPort(port)

	if err := port.SetReadTimeout(cfg.ReadTimeout); err != nil {
		return fmt.Errorf("set read timeout: %w", err)
	}
	if cfg.AssertDTR {
		if err := port.SetDTR(true); err != nil {
			return fmt.Errorf("set DTR: %w", err)
		}
	}
	if cfg.AssertRTS {
		if err := port.SetRTS(true); err != nil {
			return fmt.Errorf("set RTS: %w", err)
		}
	}

	var nextDisplayPoll time.Time
	if len(displayPollFrame) > 0 {
		nextDisplayPoll = time.Now()
	}
	var nextStatusPoll time.Time
	if len(statusPollFrame) > 0 {
		nextStatusPoll = time.Now()
	}

	buf := make([]byte, cfg.ReadSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		now := time.Now()
		if !nextDisplayPoll.IsZero() && !now.Before(nextDisplayPoll) && s.displayPollEnabled() {
			if err := s.writeFrame(ctx, displayPollFrame); err != nil {
				return fmt.Errorf("serial write display poll: %w", err)
			}
			nextDisplayPoll = now.Add(s.displayPollInterval())
		} else if !nextDisplayPoll.IsZero() && !s.displayPollEnabled() {
			nextDisplayPoll = now.Add(s.displayPollInterval())
		}

		if !nextStatusPoll.IsZero() && !now.Before(nextStatusPoll) && s.statusPollEnabled() {
			if err := s.writeFrame(ctx, statusPollFrame); err != nil {
				return fmt.Errorf("serial write status poll: %w", err)
			}
			nextStatusPoll = now.Add(s.statusPollInterval())
		} else if !nextStatusPoll.IsZero() && !s.statusPollEnabled() {
			nextStatusPoll = now.Add(s.statusPollInterval())
		}

		n, err := port.Read(buf)
		if n > 0 {
			s.connected.Store(true)
			chunk := append([]byte(nil), buf[:n]...)
			for _, frame := range statusDecoder.Push(chunk) {
				s.applyStatusFrame(frame)
			}
			for _, frame := range decoder.Push(chunk) {
				s.applyFrame(frame)
			}
		}
		if err != nil {
			if err == io.EOF {
				continue
			}
			return fmt.Errorf("serial read: %w", err)
		}
	}
}

func (s *SerialSource) writeFrame(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("button send canceled: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	port := s.currentPort()
	if port == nil {
		return transport.TransportUnavailableError()
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("button send timeout before write")
		}
		// Best-effort only: go.bug.st/serial exposes SetReadTimeout but not a true
		// write deadline API, so this may help some drivers unwind sooner without
		// strictly bounding the underlying Write call on every platform.
		if err := port.SetReadTimeout(remaining); err != nil {
			return fmt.Errorf("configure serial write timeout: %w", err)
		}
		defer func() {
			_ = port.SetReadTimeout(s.cfg.ReadTimeout)
		}()
	}

	written := make(chan error, 1)
	go func(port serial.Port) {
		_, err := port.Write(frame)
		if err != nil {
			written <- fmt.Errorf("write button frame: %w", err)
			return
		}
		written <- nil
	}(port)

	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("button send timeout after %s; underlying serial write may still complete asynchronously", tRound(timeoutForContext(ctx)))
		}
		return fmt.Errorf("button send canceled: %w", ctx.Err())
	case err := <-written:
		return err
	}
}

func (s *SerialSource) currentPort() serial.Port {
	s.portMu.RLock()
	defer s.portMu.RUnlock()
	return s.port
}

func (s *SerialSource) setPort(port serial.Port) {
	s.portMu.Lock()
	s.port = port
	s.portMu.Unlock()
}

func (s *SerialSource) applyFrame(frame []byte) {
	state, err := protocol.StateFromFrame(frame)
	if err != nil {
		s.decodeErrors.Add(1)
		s.setErr(fmt.Sprintf("decode frame: %v", err))
		return
	}

	meta := api.FrameInfo{
		Source:      "serial",
		Length:      len(frame),
		StartOffset: protocol.GuessDisplayStart(frame),
	}
	if text, err := protocol.ScreenText(frame); err == nil {
		meta.ScreenText = text
	}

	telemetry := protocol.TelemetryFromDisplayState(state, "serial")

	s.mu.Lock()
	s.latest = Update{
		State:     state,
		Telemetry: telemetry,
		Frame:     meta,
		FrameKind: "serial",
		Source:    "serial",
	}
	s.diag = IngestDiagnostics{
		FramesSeen:      s.framesSeen.Load(),
		DecodeErrors:    s.decodeErrors.Load(),
		LastFrameLength: s.lastFrameLen.Load(),
		LastFrameAt:     "",
		LastError:       s.lastErr,
		SerialPort:      s.cfg.Port,
		Connected:       s.connected.Load(),
	}
	s.mu.Unlock()

	s.framesSeen.Add(1)
	s.lastFrameLen.Store(int64(len(frame)))
	s.lastFrameAt.Store(time.Now().Unix())
}

func (s *SerialSource) applyStatusFrame(frame []byte) {
	status, err := protocol.StatusFromFrame(frame, "serial")
	if err != nil {
		s.decodeErrors.Add(1)
		s.setErr(fmt.Sprintf("decode status frame: %v", err))
		return
	}

	if s.statusState != nil {
		s.statusState.UpdateProtocolNative(status)
	}
}

func (s *SerialSource) decodePollFrame(kind string, enabled bool, frameHex string) ([]byte, error) {
	if !enabled || frameHex == "" {
		return nil, nil
	}
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		return nil, fmt.Errorf("invalid %s poll frame hex %q: %v", kind, frameHex, err)
	}
	return frame, nil
}

func (s *SerialSource) displayPollEnabled() bool {
	if s.cfg.DisplayPollEnabledFn != nil {
		return s.cfg.DisplayPollEnabledFn()
	}
	return s.cfg.DisplayPollEnabled
}

func (s *SerialSource) statusPollEnabled() bool {
	if s.cfg.StatusPollEnabledFn != nil {
		return s.cfg.StatusPollEnabledFn()
	}
	return s.cfg.StatusPollCommandEnabled
}

func (s *SerialSource) displayPollInterval() time.Duration {
	if s.cfg.DisplayPollInterval > 0 {
		return s.cfg.DisplayPollInterval
	}
	return 1 * time.Second
}

func (s *SerialSource) statusPollInterval() time.Duration {
	if s.cfg.StatusPollCommandInterval > 0 {
		return s.cfg.StatusPollCommandInterval
	}
	return 500 * time.Millisecond
}

func (s *SerialSource) setErr(msg string) {
	s.lastErrMu.Lock()
	s.lastErr = msg
	s.lastErrMu.Unlock()
}

func timeoutForContext(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return 0
}

func tRound(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d.Round(time.Millisecond)
}
