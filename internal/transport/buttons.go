package transport

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
)

const DefaultButtonTimeout = 1500 * time.Millisecond

type ButtonTransport interface {
	SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error)
}

type WakeTransport interface {
	SendWake(ctx context.Context) (api.ActionResult, error)
}

type ButtonActionError struct {
	StatusCode int
	Message    string
}

func (e *ButtonActionError) Error() string { return e.Message }

func InvalidButtonActionError(name string) *ButtonActionError {
	if name == "" {
		return &ButtonActionError{StatusCode: 400, Message: "button name is required"}
	}
	return &ButtonActionError{StatusCode: 400, Message: fmt.Sprintf("unsupported button action: %s", name)}
}

func TransportUnavailableError() *ButtonActionError {
	return &ButtonActionError{StatusCode: 503, Message: "button transport unavailable"}
}

func WakeTransportUnavailableError() *ButtonActionError {
	return &ButtonActionError{StatusCode: 503, Message: "wake transport unavailable"}
}

func ButtonStatusCode(err error) int {
	if actionErr := buttonActionError(err); actionErr != nil {
		return actionErr.StatusCode
	}
	return 500
}

func buttonActionError(err error) *ButtonActionError {
	if err == nil {
		return nil
	}
	if matched, ok := err.(*ButtonActionError); ok {
		return matched
	}
	return nil
}

type ButtonSpec struct {
	Name    string
	Code    *byte
	Safe    bool
	Comment string
}

func buttonCode(v byte) *byte {
	return &v
}

var defaultButtonSpecs = []ButtonSpec{
	{Name: "input", Code: buttonCode(0x01), Safe: true, Comment: "documented INPUT front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "band-", Code: buttonCode(0x02), Safe: true, Comment: "documented BAND- front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "band+", Code: buttonCode(0x03), Safe: true, Comment: "documented BAND+ front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "antenna", Code: buttonCode(0x04), Safe: true, Comment: "documented ANTENNA front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "l-", Code: buttonCode(0x05), Safe: true, Comment: "documented L- ATU front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "l+", Code: buttonCode(0x06), Safe: true, Comment: "documented L+ ATU front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "c-", Code: buttonCode(0x07), Safe: true, Comment: "documented C- ATU front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "c+", Code: buttonCode(0x08), Safe: true, Comment: "documented C+ ATU front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "tune", Code: buttonCode(0x09), Safe: true, Comment: "documented TUNE front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "off", Code: buttonCode(0x0a), Safe: true, Comment: "documented SWITCH OFF front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "power", Code: buttonCode(0x0b), Safe: true, Comment: "documented POWER front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "display", Code: buttonCode(0x0c), Safe: true, Comment: "documented display page cycle button from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "operate", Code: buttonCode(0x0d), Safe: true, Comment: "documented OPERATE front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "cat", Code: buttonCode(0x0e), Safe: true, Comment: "documented CAT front-panel command from the Programmer's Guide; still needs physical confirmation on real hardware"},
	{Name: "left", Code: buttonCode(0x0f), Safe: true, Comment: "documented front-panel [◄▲] key, treated as left navigation until real hardware confirms every mode"},
	{Name: "up", Code: buttonCode(0x0f), Safe: true, Comment: "documented front-panel [◄▲] key, exposed as an alias of left and still needs physical confirmation button-by-button"},
	{Name: "right", Code: buttonCode(0x10), Safe: true, Comment: "documented front-panel [▼►] key, treated as right navigation until real hardware confirms every mode"},
	{Name: "down", Code: buttonCode(0x10), Safe: true, Comment: "documented front-panel [▼►] key, exposed as an alias of right and still needs physical confirmation button-by-button"},
	{Name: "set", Code: buttonCode(0x11), Safe: true, Comment: "confirm or enter"},
	{Name: "back", Safe: false, Comment: "blocked because the current docs do not establish a distinct back command separate from the navigation keys"},
	{Name: "on", Safe: false, Comment: "blocked because the current docs expose POWER and SWITCH OFF but do not establish a distinct ON command"},
	{Name: "standby", Safe: false, Comment: "blocked because the current docs do not establish a standalone standby button code in the newer direct-command table"},
}

func DefaultButtonMap() map[string]ButtonSpec {
	out := make(map[string]ButtonSpec, len(defaultButtonSpecs))
	for _, spec := range defaultButtonSpecs {
		out[spec.Name] = spec
	}
	return out
}

type LocalButtonTransport struct {
	portName string
	opener   serial.PortOpener
	timeout  time.Duration
	specs    map[string]ButtonSpec
}

type WakeSequencePort interface {
	SetDTR(bool) error
	SetRTS(bool) error
}

type WakeSequence struct {
	Hold time.Duration
}

func DefaultWakeSequence() WakeSequence {
	return WakeSequence{Hold: time.Second}
}

func (s WakeSequence) normalized() WakeSequence {
	if s.Hold <= 0 {
		s.Hold = DefaultWakeSequence().Hold
	}
	return s
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wake canceled: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

func RunWakeSequence(ctx context.Context, port WakeSequencePort, seq WakeSequence) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("wake canceled: %w", err)
	}
	seq = seq.normalized()
	if err := port.SetDTR(true); err != nil {
		return fmt.Errorf("set DTR high before wake: %w", err)
	}
	if err := port.SetDTR(false); err != nil {
		return fmt.Errorf("drop DTR for wake: %w", err)
	}
	if err := port.SetRTS(true); err != nil {
		return fmt.Errorf("set RTS high for wake: %w", err)
	}
	if err := sleepWithContext(ctx, seq.Hold); err != nil {
		return err
	}
	if err := port.SetDTR(true); err != nil {
		return fmt.Errorf("reassert DTR after wake: %w", err)
	}
	if err := port.SetRTS(false); err != nil {
		return fmt.Errorf("drop RTS after wake: %w", err)
	}
	return nil
}

type LocalWakeTransport struct {
	portName string
	opener   serial.PortOpener
	timeout  time.Duration
	sequence WakeSequence
}

func NewLocalButtonTransport(portName string, opener serial.PortOpener, timeout time.Duration) *LocalButtonTransport {
	if timeout <= 0 {
		timeout = DefaultButtonTimeout
	}
	if opener == nil {
		opener = serial.OpenRealPort{}
	}
	return &LocalButtonTransport{portName: strings.TrimSpace(portName), opener: opener, timeout: timeout, specs: DefaultButtonMap()}
}

func NewLocalWakeTransport(portName string, opener serial.PortOpener, timeout time.Duration) *LocalWakeTransport {
	if timeout <= 0 {
		timeout = DefaultButtonTimeout
	}
	if opener == nil {
		opener = serial.OpenRealPort{}
	}
	return &LocalWakeTransport{portName: strings.TrimSpace(portName), opener: opener, timeout: timeout, sequence: DefaultWakeSequence()}
}

func (t *LocalButtonTransport) SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error) {
	action = action.Normalized()
	spec, ok := t.specs[action.Name]
	if !ok || !spec.Safe || spec.Code == nil {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial"}, InvalidButtonActionError(action.Name)
	}
	if t.portName == "" {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial"}, TransportUnavailableError()
	}
	frame := frameForCode(*spec.Code)
	writeCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	if err := writeFrame(writeCtx, t.opener, t.portName, frame); err != nil {
		return api.ActionResult{Name: action.Name, Queued: false, Sent: false, Transport: "serial", FrameHex: hex.EncodeToString(frame)}, err
	}
	return api.ActionResult{Name: action.Name, Queued: false, Sent: true, Transport: "serial", FrameHex: hex.EncodeToString(frame)}, nil
}

func (t *LocalWakeTransport) SendWake(ctx context.Context) (api.ActionResult, error) {
	if t.portName == "" {
		return api.ActionResult{Name: "wake", Queued: false, Sent: false, Transport: "serial-wake"}, WakeTransportUnavailableError()
	}
	writeCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	if err := runWake(writeCtx, t.opener, t.portName, t.sequence); err != nil {
		return api.ActionResult{Name: "wake", Queued: false, Sent: false, Transport: "serial-wake"}, err
	}
	return api.ActionResult{Name: "wake", Queued: false, Sent: true, Transport: "serial-wake"}, nil
}

func frameForCode(code byte) []byte {
	return []byte{0x55, 0x55, 0x55, 0x01, code, code}
}

func writeFrame(ctx context.Context, opener serial.PortOpener, portName string, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("button send canceled: %w", err)
	}
	port, err := opener.Open(portName, 115200)
	if err != nil {
		return fmt.Errorf("open button transport on %s: %w", portName, err)
	}
	defer port.Close()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("button send timeout before write")
		}
		if err := port.SetReadTimeout(remaining); err != nil {
			return fmt.Errorf("configure button transport timeout: %w", err)
		}
	}
	written := make(chan error, 1)
	go func() {
		_, err := port.Write(frame)
		if err != nil {
			written <- fmt.Errorf("write button frame: %w", err)
			return
		}
		written <- nil
	}()
	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("button send timeout after %s", tRound(timeoutForContext(ctx)))
		}
		return fmt.Errorf("button send canceled: %w", ctx.Err())
	case err := <-written:
		return err
	}
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

func runWake(ctx context.Context, opener serial.PortOpener, portName string, seq WakeSequence) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("wake canceled: %w", err)
	}
	port, err := opener.Open(portName, 115200)
	if err != nil {
		return fmt.Errorf("open wake transport on %s: %w", portName, err)
	}
	defer port.Close()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("wake timeout before serial open")
		}
		if err := port.SetReadTimeout(remaining); err != nil {
			return fmt.Errorf("configure wake transport timeout: %w", err)
		}
	}
	if err := sleepWithContext(ctx, 300*time.Millisecond); err != nil {
		return err
	}
	if err := RunWakeSequence(ctx, port, seq); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("wake timeout after %s", tRound(timeoutForContext(ctx)))
		}
		return err
	}
	return nil
}
