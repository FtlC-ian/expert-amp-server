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
	PollingMode               string
	PollInterval              time.Duration
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
	PollingModeFn             func() string
	DisplayPollEnabledFn      func() bool
	StatusPollEnabledFn       func() bool
	LCDFlagDebug              bool
}

// Defaults returns a SerialSourceConfig with sensible SPE Expert defaults.
func DefaultSerialSourceConfig(port string) SerialSourceConfig {
	return SerialSourceConfig{
		Port:                      port,
		BaudRate:                  115200,
		ReadTimeout:               250 * time.Millisecond,
		ReadSize:                  512,
		PollingMode:               "both",
		PollInterval:              125 * time.Millisecond,
		DisplayPollEnabled:        true,
		DisplayPollInterval:       125 * time.Millisecond,
		DisplayPollFrameHex:       hex.EncodeToString(protocol.DisplayPollCommand),
		StatusPollCommandEnabled:  true,
		StatusPollCommandInterval: 125 * time.Millisecond,
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

	flagDebugMu      sync.Mutex
	lastUnknownFlags uint16
	haveUnknownFlags bool

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

	scheduler := newSerialPollScheduler(time.Now(), s.pollingMode(), s.pollInterval(), len(displayPollFrame) > 0, len(statusPollFrame) > 0)

	buf := make([]byte, cfg.ReadSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		now := time.Now()
		if kind := scheduler.nextDue(now, s.pollingMode()); kind != "" {
			var frame []byte
			if kind == "status" {
				frame = statusPollFrame
			} else {
				frame = displayPollFrame
			}
			if err := s.writeFrame(ctx, frame); err != nil {
				return fmt.Errorf("serial write %s poll: %w", kind, err)
			}
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
		StartOffset: protocol.LCDDataOffset(frame),
	}
	if flags, ok := protocol.LCDFlagsFromFrame(frame); ok {
		meta.LCDFlags = apiLCDFlags(flags)
		s.logLCDFlagDebug(flags, meta.ScreenText)
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

func (s *SerialSource) logLCDFlagDebug(flags *protocol.LCDFlags, screenText string) {
	if !s.cfg.LCDFlagDebug || flags == nil || !flags.Validated() {
		return
	}
	unknown := flags.Decoded &^ protocol.KnownLCDLEDMask

	s.flagDebugMu.Lock()
	changed := !s.haveUnknownFlags || unknown != s.lastUnknownFlags
	if changed {
		s.lastUnknownFlags = unknown
		s.haveUnknownFlags = true
	}
	s.flagDebugMu.Unlock()

	if !changed {
		return
	}
	leds := flags.LEDs
	if leds == nil {
		log.Printf("lcd flag debug: unknown=0x%04x decoded=0x%04x rawInverted=0x%04x checksumValid=%v", unknown, flags.Decoded, flags.RawInverted, flags.ChecksumValid)
		return
	}
	log.Printf("lcd flag debug: unknown=0x%04x decoded=0x%04x rawInverted=0x%04x leds={tx:%t op:%t set:%t tune:%t} screen=%q", unknown, flags.Decoded, flags.RawInverted, leds.TX, leds.Operate, leds.Set, leds.Tune, firstScreenLine(screenText))
}

func firstScreenLine(screenText string) string {
	line, _, _ := strings.Cut(screenText, "\n")
	return strings.TrimSpace(line)
}

func apiLCDFlags(flags *protocol.LCDFlags) *api.LCDFlags {
	if flags == nil {
		return nil
	}
	out := &api.LCDFlags{
		RawInverted:     flags.RawInverted,
		Decoded:         flags.Decoded,
		ChecksumPresent: flags.ChecksumPresent,
		ChecksumValid:   flags.ChecksumValid,
	}
	if flags.LEDs != nil {
		out.LEDs = &api.LCDLEDs{
			TX:      flags.LEDs.TX,
			Operate: flags.LEDs.Operate,
			Set:     flags.LEDs.Set,
			Tune:    flags.LEDs.Tune,
		}
	}
	return out
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

func (s *SerialSource) pollingMode() string {
	if s.cfg.PollingModeFn != nil {
		return s.cfg.PollingModeFn()
	}
	if s.cfg.PollingMode != "" {
		return s.cfg.PollingMode
	}
	return legacySerialPollingMode(s.displayPollEnabled(), s.statusPollEnabled())
}

func (s *SerialSource) pollInterval() time.Duration {
	if s.cfg.PollInterval > 0 {
		return s.cfg.PollInterval
	}
	if s.cfg.StatusPollCommandInterval > 0 {
		return s.cfg.StatusPollCommandInterval
	}
	if s.cfg.DisplayPollInterval > 0 {
		return s.cfg.DisplayPollInterval
	}
	return 125 * time.Millisecond
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

func legacySerialPollingMode(display, status bool) string {
	switch {
	case display && status:
		return "both"
	case status:
		return "status"
	case display:
		return "display"
	default:
		return "off"
	}
}

type serialPollScheduler struct {
	interval     time.Duration
	halfInterval time.Duration
	hasDisplay   bool
	hasStatus    bool
	next         time.Time
	phase        string
	mode         string
}

func newSerialPollScheduler(now time.Time, mode string, interval time.Duration, hasDisplay, hasStatus bool) *serialPollScheduler {
	if interval <= 0 {
		interval = 125 * time.Millisecond
	}
	half := interval / 2
	if half <= 0 {
		half = interval
	}
	s := &serialPollScheduler{interval: interval, halfInterval: half, hasDisplay: hasDisplay, hasStatus: hasStatus}
	s.reset(now, mode)
	return s
}

func (s *serialPollScheduler) reset(now time.Time, mode string) {
	s.mode = normalizeSerialPollingMode(mode)
	s.next = time.Time{}
	s.phase = ""
	s.prime(now)
}

func (s *serialPollScheduler) prime(now time.Time) {
	s.next = time.Time{}
	s.phase = ""
	s.mode = s.effectiveMode(s.mode)
	switch s.mode {
	case "status":
		s.phase = "status"
		s.next = now
	case "display":
		s.phase = "display"
		s.next = now
	case "both":
		s.phase = "status"
		s.next = now
	}
}

func (s *serialPollScheduler) nextDue(now time.Time, mode string) string {
	mode = normalizeSerialPollingMode(mode)
	if mode != s.mode {
		s.reset(now, mode)
	}
	if s.next.IsZero() || now.Before(s.next) {
		return ""
	}
	kind := s.phase
	s.advance(now)
	return kind
}

func (s *serialPollScheduler) advance(now time.Time) {
	s.mode = s.effectiveMode(s.mode)
	if s.mode == "off" {
		s.next = time.Time{}
		s.phase = ""
		return
	}
	if s.mode == "both" {
		if s.phase == "status" {
			s.phase = "display"
		} else {
			s.phase = "status"
		}
		s.next = s.next.Add(s.halfInterval)
	} else {
		s.phase = s.mode
		s.next = s.next.Add(s.interval)
	}
	for !s.next.After(now) {
		if s.mode == "both" {
			s.next = s.next.Add(s.halfInterval)
			if s.phase == "status" {
				s.phase = "display"
			} else {
				s.phase = "status"
			}
		} else {
			s.next = s.next.Add(s.interval)
		}
	}
}

func (s *serialPollScheduler) effectiveMode(mode string) string {
	switch mode {
	case "both":
		if s.hasStatus && s.hasDisplay {
			return "both"
		}
		if s.hasStatus {
			return "status"
		}
		if s.hasDisplay {
			return "display"
		}
	case "status":
		if s.hasStatus {
			return "status"
		}
	case "display":
		if s.hasDisplay {
			return "display"
		}
	}
	return "off"
}

func normalizeSerialPollingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "off", "status", "display", "both":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "both"
	}
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
