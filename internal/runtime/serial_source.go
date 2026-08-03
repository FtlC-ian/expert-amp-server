package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/fanpolicy"
	"github.com/FtlC-ian/expert-amp-server/internal/menudebug"
	"github.com/FtlC-ian/expert-amp-server/internal/monitoring"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
	"github.com/FtlC-ian/expert-amp-server/internal/tempunit"
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
	TemperatureUnitFn         func() string
	LCDFlagDebug              bool
	IOTimeout                 time.Duration
	ReconnectBackoff          time.Duration
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
		TemperatureUnitFn:         func() string { return tempunit.Celsius },
		IOTimeout:                 transport.DefaultButtonTimeout,
		ReconnectBackoff:          2 * time.Second,
	}
}

// IngestDiagnostics holds counters and timestamps for the live serial path.
type IngestDiagnostics struct {
	FramesSeen      int64  `json:"framesSeen"`
	DecodeErrors    int64  `json:"decodeErrors"`
	LastFrameLength int64  `json:"lastFrameLength"`
	LastFrameAt     string `json:"lastFrameAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	LastErrorAt     string `json:"lastErrorAt,omitempty"`
	LastRecoveredAt string `json:"lastRecoveredAt,omitempty"`
	Reconnects      int64  `json:"reconnects"`
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

	framesSeen      atomic.Int64
	decodeErrors    atomic.Int64
	lastFrameLen    atomic.Int64
	lastFrameAt     atomic.Int64
	connected       atomic.Bool
	reconnects      atomic.Int64
	lastErrorAt     atomic.Int64
	lastRecoveredAt atomic.Int64

	lastErrMu          sync.RWMutex
	lastErr            string
	transportErrActive bool

	portMu               sync.RWMutex
	port                 serial.Port
	portGeneration       uint64
	statusPortGeneration uint64
	statusModel          string
	statusOperatingState string
	statusTX             bool
	statusTXKnown        bool
	portDone             chan struct{}
	lifecycleMu          sync.Mutex
	reconnectNow         chan struct{}

	writeMu          sync.Mutex
	specs            map[string]transport.ButtonSpec
	safetyController *monitoring.Controller
	safetySettings   func() monitoring.ControlSettings
	fanController    *fanpolicy.Controller
	fanSettings      func() fanpolicy.Settings
	menuDebug        *menudebug.Controller
	menuStatusGen    atomic.Uint64

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
		cfg:          cfg,
		opener:       opener,
		latest:       initial,
		statusState:  NewStatusState(api.Status{Telemetry: initial.Telemetry}),
		specs:        transport.DefaultButtonMap(),
		reconnectNow: make(chan struct{}, 1),
	}
}

// Start begins the background serial read loop. It returns immediately; the
// loop runs until [SerialSource.Stop] is called or a fatal error occurs.
func (s *SerialSource) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.readLoop(ctx)
	go s.safetyContactLoop(ctx)
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

func (s *SerialSource) ConfigureSafetyController(controller *monitoring.Controller, settings func() monitoring.ControlSettings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.safetyController = controller
	s.safetySettings = settings
	s.mu.Unlock()
}

func (s *SerialSource) ConfigureFanPolicyController(controller *fanpolicy.Controller, settings func() fanpolicy.Settings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.fanController = controller
	s.fanSettings = settings
	s.mu.Unlock()
	if evidence := s.SerialSessionEvidence(); controller != nil && evidence.Active {
		controller.ObserveSerialSession(evidence.Generation)
	}
}

func (s *SerialSource) ConfigureMenuDebugController(controller *menudebug.Controller) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.menuDebug = controller
	s.mu.Unlock()
	if evidence := s.SerialSessionEvidence(); controller != nil && evidence.Active {
		controller.ObserveSerialSession(evidence.Generation)
	}
}

// SerialSessionEvidence identifies the active live port and the session that
// supplied the latest accepted protocol status frame.
type SerialSessionEvidence struct {
	Generation       uint64
	StatusGeneration uint64
	Active           bool
}

func (s *SerialSource) SerialSessionEvidence() SerialSessionEvidence {
	if s == nil {
		return SerialSessionEvidence{}
	}
	s.portMu.RLock()
	defer s.portMu.RUnlock()
	return SerialSessionEvidence{Generation: s.portGeneration, StatusGeneration: s.statusPortGeneration, Active: s.port != nil}
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
	d.Reconnects = s.reconnects.Load()
	d.SerialPort = s.cfg.Port
	if ts := s.lastFrameAt.Load(); ts > 0 {
		d.LastFrameAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	s.lastErrMu.RLock()
	d.LastError = s.lastErr
	s.lastErrMu.RUnlock()
	if ts := s.lastErrorAt.Load(); ts > 0 {
		d.LastErrorAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	if ts := s.lastRecoveredAt.Load(); ts > 0 {
		d.LastRecoveredAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	return d
}

// SendButton writes a safe action frame through the currently held live serial port.
func (s *SerialSource) SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error) {
	return s.sendButton(ctx, action, transport.SerialSessionWriteAuthorization{})
}

// SendButtonForSerialSession captures and verifies the current port under the
// same write lock used for dispatch, so a reconnect can never redirect an
// authorized command to a replacement amplifier.
func (s *SerialSource) SendButtonForSerialSession(ctx context.Context, action api.ButtonAction, authorization transport.SerialSessionWriteAuthorization) (api.ActionResult, error) {
	if authorization.SessionGeneration == 0 || strings.TrimSpace(authorization.Model) == "" {
		return api.ActionResult{Name: action.Name}, errors.New("serial session and model are required")
	}
	return s.sendButton(ctx, action, authorization)
}

func (s *SerialSource) sendButton(ctx context.Context, action api.ButtonAction, authorization transport.SerialSessionWriteAuthorization) (api.ActionResult, error) {
	action = action.Normalized()
	spec, ok := s.specs[action.Name]
	if !ok || !spec.Safe || spec.Code == nil {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial-live"}, transport.InvalidButtonActionError(action.Name)
	}
	frame := []byte{0x55, 0x55, 0x55, 0x01, *spec.Code, *spec.Code}
	if err := s.writeFrameForSerialSession(ctx, frame, authorization); err != nil {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial-live", FrameHex: hex.EncodeToString(frame)}, err
	}
	return api.ActionResult{Name: action.Name, Queued: false, Sent: true, Transport: "serial-live", FrameHex: hex.EncodeToString(frame)}, nil
}

func (s *SerialSource) SendWake(ctx context.Context) (api.ActionResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.lifecycleMu.Lock()
	defer func() {
		s.lifecycleMu.Unlock()
		s.signalReconnect()
	}()

	if strings.TrimSpace(s.cfg.Port) == "" {
		return api.ActionResult{Name: "wake", Queued: false, Sent: false, Transport: "serial-live-wake"}, transport.WakeTransportUnavailableError()
	}
	if err := s.retireCurrentSession(ctx); err != nil {
		return api.ActionResult{Name: "wake", Queued: false, Sent: false, Transport: "serial-live-wake"}, err
	}
	opener := s.opener
	if opener == nil {
		opener = serial.OpenRealPort{}
	}
	wake := transport.NewLocalWakeTransport(s.cfg.Port, s.cfg.BaudRate, opener, timeoutForContext(ctx))
	result, err := wake.SendWake(ctx)
	result.Transport = "serial-live-wake"
	return result, err
}

func (s *SerialSource) readLoop(ctx context.Context) {
	s.connected.Store(false)

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
			s.connected.Store(false)
			return
		}

		s.lifecycleMu.Lock()
		if ctx.Err() != nil {
			s.lifecycleMu.Unlock()
			s.connected.Store(false)
			return
		}
		port, err := opener.Open(serialCfg.Port, serialCfg.BaudRate)
		var generation uint64
		var done chan struct{}
		if err != nil {
			err = fmt.Errorf("open serial %s: %w", serialCfg.Port, err)
		} else {
			generation, done = s.beginSession(port)
		}
		s.lifecycleMu.Unlock()
		if err == nil {
			decoder := protocol.NewDisplayStreamDecoder(protocol.StreamDecoderConfig{
				MinFrameLen: s.cfg.MinFrameLen,
				MaxBuffer:   s.cfg.MaxBuffer,
			})
			statusDecoder := protocol.NewStatusStreamDecoder()
			err = s.readFromPort(ctx, port, generation, serialCfg, decoder, statusDecoder, displayPollFrame, statusPollFrame)
			s.endSession(generation, done)
		}
		if err == nil {
			// Context canceled.
			s.connected.Store(false)
			return
		}
		s.connected.Store(false)
		s.setTransportErr(err.Error())
		log.Printf("serial source read error: %v", err)

		// Backoff before reconnect.
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.reconnectBackoff()):
		case <-s.reconnectNow:
		}
	}
}

func (s *SerialSource) readFromPort(ctx context.Context, port serial.Port, sessionGeneration uint64, cfg serial.Config, decoder *protocol.DisplayStreamDecoder, statusDecoder *protocol.StatusStreamDecoder, displayPollFrame []byte, statusPollFrame []byte) error {
	defer port.Close()

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
			chunk := append([]byte(nil), buf[:n]...)
			for _, frame := range statusDecoder.Push(chunk) {
				s.applyStatusFrameFromSession(frame, sessionGeneration)
			}
			for _, frame := range decoder.Push(chunk) {
				s.applyFrameFromSession(frame, sessionGeneration)
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
	return s.writeFrameForSerialSession(ctx, frame, transport.SerialSessionWriteAuthorization{})
}

func (s *SerialSource) writeFrameForSerialSession(ctx context.Context, frame []byte, authorization transport.SerialSessionWriteAuthorization) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("button send canceled: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.portMu.RLock()
	port := s.port
	currentGeneration := s.portGeneration
	statusGeneration := s.statusPortGeneration
	statusModel := s.statusModel
	statusOperatingState := s.statusOperatingState
	statusTX := s.statusTX
	statusTXKnown := s.statusTXKnown
	s.portMu.RUnlock()
	if port == nil {
		return transport.TransportUnavailableError()
	}
	expectedOperatingState := strings.TrimSpace(authorization.ExpectedOperatingState)
	if expectedOperatingState == "" {
		expectedOperatingState = "standby"
	}
	if authorization.SessionGeneration != 0 && (currentGeneration != authorization.SessionGeneration || statusGeneration != authorization.SessionGeneration || !strings.EqualFold(strings.TrimSpace(statusModel), strings.TrimSpace(authorization.Model)) || !strings.EqualFold(strings.TrimSpace(statusOperatingState), expectedOperatingState) || !statusTXKnown || statusTX) {
		return fmt.Errorf("serial session, amplifier model, or expected %s/RX status changed before button write", strings.ToUpper(expectedOperatingState))
	}
	writeCtx, cancel := context.WithTimeout(ctx, s.ioTimeout())
	defer cancel()
	if deadline, ok := writeCtx.Deadline(); ok {
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
	case <-writeCtx.Done():
		s.retirePortIfCurrent(port)
		if writeCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("button send timeout after %s; serial session retired", tRound(s.ioTimeout()))
		}
		return fmt.Errorf("button send canceled: %w", writeCtx.Err())
	case err := <-written:
		return err
	}
}

func (s *SerialSource) currentPort() serial.Port {
	s.portMu.RLock()
	defer s.portMu.RUnlock()
	return s.port
}

func (s *SerialSource) beginSession(port serial.Port) (uint64, chan struct{}) {
	s.portMu.Lock()
	s.portGeneration++
	generation := s.portGeneration
	s.statusPortGeneration = 0
	s.statusModel = ""
	s.statusOperatingState = ""
	s.statusTX = false
	s.statusTXKnown = false
	done := make(chan struct{})
	s.port = port
	s.portDone = done
	s.portMu.Unlock()
	s.mu.RLock()
	fanController := s.fanController
	menuDebug := s.menuDebug
	s.mu.RUnlock()
	if fanController != nil {
		fanController.ObserveSerialSession(generation)
	}
	if menuDebug != nil {
		menuDebug.ObserveSerialSession(generation)
	}
	return generation, done
}

func (s *SerialSource) endSession(generation uint64, done chan struct{}) {
	s.portMu.Lock()
	if s.portGeneration == generation {
		s.port = nil
		s.portDone = nil
	}
	s.portMu.Unlock()
	close(done)
}

func (s *SerialSource) retirePortIfCurrent(port serial.Port) {
	s.portMu.Lock()
	if s.port == port {
		s.port = nil
		s.connected.Store(false)
	}
	s.portMu.Unlock()
	_ = port.Close()
}

func (s *SerialSource) retireCurrentSession(ctx context.Context) error {
	s.portMu.Lock()
	port := s.port
	done := s.portDone
	if port != nil {
		s.port = nil
		s.connected.Store(false)
	}
	s.portMu.Unlock()
	if port == nil && done == nil {
		return nil
	}
	if port != nil {
		if err := port.Close(); err != nil {
			return fmt.Errorf("close live serial session for wake: %w", err)
		}
	}
	if done == nil {
		return nil
	}
	wait := time.NewTimer(s.sessionRetireTimeout())
	defer wait.Stop()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wake canceled while waiting for live serial session: %w", ctx.Err())
	case <-wait.C:
		return fmt.Errorf("timeout waiting for live serial session to stop before wake")
	}
}

func (s *SerialSource) signalReconnect() {
	select {
	case s.reconnectNow <- struct{}{}:
	default:
	}
}

func (s *SerialSource) applyFrame(frame []byte) {
	s.applyFrameFromSession(frame, 0)
}

func (s *SerialSource) applyFrameFromSession(frame []byte, sessionGeneration uint64) {
	state, err := protocol.StateFromFrame(frame)
	if err != nil {
		s.decodeErrors.Add(1)
		s.setErr(fmt.Sprintf("decode frame: %v", err))
		return
	}
	if sessionGeneration != 0 {
		s.portMu.RLock()
		current := s.port != nil && s.portGeneration == sessionGeneration
		s.portMu.RUnlock()
		if !current {
			return
		}
	}
	s.markHealthy()

	meta := api.FrameInfo{
		Source:      "serial",
		Length:      len(frame),
		StartOffset: protocol.LCDDataOffset(frame),
	}
	var displayTX *bool
	var displayOperate *bool
	if flags, ok := protocol.LCDFlagsFromFrame(frame); ok {
		meta.LCDFlags = apiLCDFlags(flags)
		s.logLCDFlagDebug(flags, meta.ScreenText)
		if flags.Validated() && flags.LEDs != nil {
			tx := flags.LEDs.TX
			operate := flags.LEDs.Operate
			displayTX = &tx
			displayOperate = &operate
		}
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
		SerialPort:      s.cfg.Port,
		Connected:       s.connected.Load(),
	}
	s.mu.Unlock()

	generation := uint64(s.framesSeen.Add(1))
	s.lastFrameLen.Store(int64(len(frame)))
	s.lastFrameAt.Store(time.Now().Unix())

	s.mu.RLock()
	fanController := s.fanController
	menuDebug := s.menuDebug
	s.mu.RUnlock()
	if fanController != nil {
		fanController.ObserveDisplay(fanpolicy.DisplayObservation{
			State: state, Generation: generation, TX: displayTX, Operate: displayOperate, SerialSessionGeneration: sessionGeneration,
		})
	}
	if menuDebug != nil {
		checksumValid := meta.LCDFlags != nil && meta.LCDFlags.ChecksumPresent && meta.LCDFlags.ChecksumValid
		if sessionGeneration == 0 {
			menuDebug.ObserveDisplay(state, generation, checksumValid, displayTX, displayOperate)
		} else {
			menuDebug.ObserveDisplayFromSerialSession(state, generation, checksumValid, displayTX, displayOperate, sessionGeneration)
		}
	}
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
	s.applyStatusFrameFromSession(frame, 0)
}

func (s *SerialSource) applyStatusFrameFromSession(frame []byte, sessionGeneration uint64) {
	temperatureUnit := tempunit.Celsius
	if s.cfg.TemperatureUnitFn != nil {
		temperatureUnit = s.cfg.TemperatureUnitFn()
	}
	status, err := protocol.StatusFromFrameWithTemperatureUnit(frame, "serial", temperatureUnit)
	if err != nil {
		s.decodeErrors.Add(1)
		s.setErr(fmt.Sprintf("decode status frame: %v", err))
		return
	}
	if sessionGeneration != 0 {
		s.writeMu.Lock()
		s.portMu.Lock()
		if s.port == nil || s.portGeneration != sessionGeneration {
			s.portMu.Unlock()
			s.writeMu.Unlock()
			return
		}
		s.statusPortGeneration = sessionGeneration
		// A newly accepted status frame invalidates the prior identity before
		// any other observer can see the update. Dispatch uses writeMu too, so
		// either the prior authorized write finishes first or it fails closed.
		s.statusModel = ""
		s.statusOperatingState = ""
		s.statusTX = false
		s.statusTXKnown = false
		s.portMu.Unlock()
		s.writeMu.Unlock()
	}
	s.markHealthy()

	if s.statusState != nil {
		s.statusState.UpdateProtocolNative(status)
	}
	controllerStatus := status
	controllerStatus.RecentContact = true
	controllerStatus.LastContactAt = time.Now().UTC().Format(time.RFC3339)
	s.mu.RLock()
	safetyController := s.safetyController
	safetySettings := s.safetySettings
	fanController := s.fanController
	fanSettings := s.fanSettings
	menuDebug := s.menuDebug
	s.mu.RUnlock()
	if sessionGeneration != 0 {
		s.writeMu.Lock()
		s.portMu.Lock()
		if s.port != nil && s.portGeneration == sessionGeneration {
			s.statusPortGeneration = sessionGeneration
			s.statusModel = strings.TrimSpace(controllerStatus.ModelName)
			s.statusOperatingState = strings.TrimSpace(controllerStatus.OperatingState)
			if controllerStatus.TX != nil {
				s.statusTX = *controllerStatus.TX
				s.statusTXKnown = true
			} else {
				s.statusTXKnown = false
			}
		}
		s.portMu.Unlock()
		s.writeMu.Unlock()
	}
	if safetyController != nil && safetySettings != nil {
		safetyController.Observe(context.Background(), controllerStatus, safetySettings())
	}
	if fanController != nil && fanSettings != nil {
		if sessionGeneration == 0 {
			fanController.Observe(controllerStatus, fanSettings())
		} else {
			fanController.ObserveFromSerialSession(controllerStatus, fanSettings(), sessionGeneration)
		}
	}
	if menuDebug != nil {
		statusGeneration := s.menuStatusGen.Add(1)
		if sessionGeneration == 0 {
			menuDebug.ObserveStatus(controllerStatus, statusGeneration)
		} else {
			menuDebug.ObserveStatusFromSerialSession(controllerStatus, statusGeneration, sessionGeneration)
		}
	}
}

func (s *SerialSource) safetyContactLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.RLock()
			safetyController := s.safetyController
			fanController := s.fanController
			fanSettings := s.fanSettings
			menuDebug := s.menuDebug
			s.mu.RUnlock()
			if safetyController == nil && fanController == nil && menuDebug == nil {
				continue
			}
			last := s.statusState.protocolUpdatedAt()
			if !last.IsZero() && time.Since(last) > RecentContactWindow {
				if safetyController != nil {
					safetyController.ObserveContactLoss()
				}
				if fanController != nil && fanSettings != nil {
					status := s.statusState.CurrentProtocolNativeWithContact()
					if !status.RecentContact {
						fanController.Observe(status, fanSettings())
					}
				}
			}
			if fanController != nil {
				fanController.Tick(time.Now())
			}
			if menuDebug != nil {
				menuDebug.Tick(time.Now())
			}
		}
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
	s.transportErrActive = false
	s.lastErrMu.Unlock()
	s.lastErrorAt.Store(time.Now().Unix())
}

func (s *SerialSource) setTransportErr(msg string) {
	s.lastErrMu.Lock()
	s.lastErr = msg
	s.transportErrActive = true
	s.lastErrMu.Unlock()
	s.lastErrorAt.Store(time.Now().Unix())
}

func (s *SerialSource) markHealthy() {
	s.connected.Store(true)
	s.lastErrMu.Lock()
	if s.transportErrActive {
		s.lastErr = ""
		s.transportErrActive = false
		s.lastErrorAt.Store(0)
		s.lastRecoveredAt.Store(time.Now().Unix())
		s.reconnects.Add(1)
	}
	s.lastErrMu.Unlock()
}

func (s *SerialSource) ioTimeout() time.Duration {
	if s.cfg.IOTimeout > 0 {
		return s.cfg.IOTimeout
	}
	return transport.DefaultButtonTimeout
}

func (s *SerialSource) reconnectBackoff() time.Duration {
	if s.cfg.ReconnectBackoff > 0 {
		return s.cfg.ReconnectBackoff
	}
	return 2 * time.Second
}

func (s *SerialSource) sessionRetireTimeout() time.Duration {
	timeout := s.cfg.ReadTimeout + 250*time.Millisecond
	if timeout < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	if timeout > s.ioTimeout() {
		return s.ioTimeout()
	}
	return timeout
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
