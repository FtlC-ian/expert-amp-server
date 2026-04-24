package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
)

type mockPort struct {
	written    [][]byte
	writeErr   error
	blockWrite chan struct{}
	dtr        []bool
	rts        []bool
}

func (m *mockPort) Read([]byte) (int, error) { return 0, nil }
func (m *mockPort) Write(buf []byte) (int, error) {
	if m.blockWrite != nil {
		<-m.blockWrite
	}
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	m.written = append(m.written, append([]byte(nil), buf...))
	return len(buf), nil
}
func (m *mockPort) Close() error                       { return nil }
func (m *mockPort) SetReadTimeout(time.Duration) error { return nil }
func (m *mockPort) SetDTR(v bool) error                { m.dtr = append(m.dtr, v); return nil }
func (m *mockPort) SetRTS(v bool) error                { m.rts = append(m.rts, v); return nil }

type mockOpener struct {
	port serial.Port
	err  error
}

func (m *mockOpener) Open(string, int) (serial.Port, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.port, nil
}

func TestLocalButtonTransportSendsSafeButton(t *testing.T) {
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
		{name: "set", actionName: " Set ", want: []byte{0x55, 0x55, 0x55, 0x01, 0x11, 0x11}},
		{name: "up alias", actionName: "up", want: []byte{0x55, 0x55, 0x55, 0x01, 0x0f, 0x0f}},
		{name: "down alias", actionName: "down", want: []byte{0x55, 0x55, 0x55, 0x01, 0x10, 0x10}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			port := &mockPort{}
			transport := NewLocalButtonTransport("/dev/ttyTEST0", &mockOpener{port: port}, 500*time.Millisecond)

			result, err := transport.SendButton(context.Background(), api.ButtonAction{Name: tc.actionName})
			if err != nil {
				t.Fatalf("SendButton error: %v", err)
			}
			if !result.Sent {
				t.Fatalf("unexpected result: %+v", result)
			}
			if len(port.written) != 1 {
				t.Fatalf("writes = %d, want 1", len(port.written))
			}
			got := port.written[0]
			if len(got) != len(tc.want) {
				t.Fatalf("frame len = %d, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("frame[%d] = 0x%02x, want 0x%02x", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestLocalButtonTransportRejectsUnsafeOrUnknownButtons(t *testing.T) {
	transport := NewLocalButtonTransport("/dev/ttyTEST0", &mockOpener{port: &mockPort{}}, time.Second)
	for _, name := range []string{"back", "on", "standby", "bogus"} {
		_, err := transport.SendButton(context.Background(), api.ButtonAction{Name: name})
		actionErr := buttonActionError(err)
		if actionErr == nil || actionErr.StatusCode != 400 {
			t.Fatalf("%s error = %v, want 400 button action error", name, err)
		}
	}
}

func TestLocalButtonTransportRequiresPort(t *testing.T) {
	transport := NewLocalButtonTransport("", nil, time.Second)
	_, err := transport.SendButton(context.Background(), api.ButtonAction{Name: "set"})
	actionErr := buttonActionError(err)
	if actionErr == nil || actionErr.StatusCode != 503 {
		t.Fatalf("error = %v, want 503 button transport unavailable", err)
	}
}

func TestLocalButtonTransportSurfacesWriteErrors(t *testing.T) {
	transport := NewLocalButtonTransport("/dev/ttyTEST0", &mockOpener{port: &mockPort{writeErr: errors.New("serial offline")}}, time.Second)
	_, err := transport.SendButton(context.Background(), api.ButtonAction{Name: "left"})
	if err == nil || err.Error() != "write button frame: serial offline" {
		t.Fatalf("error = %v, want write button frame error", err)
	}
}

func TestLocalButtonTransportTimesOutBlockedWrite(t *testing.T) {
	block := make(chan struct{})
	transport := NewLocalButtonTransport("/dev/ttyTEST0", &mockOpener{port: &mockPort{blockWrite: block}}, 20*time.Millisecond)
	_, err := transport.SendButton(context.Background(), api.ButtonAction{Name: "right"})
	close(block)
	if err == nil || err.Error() == "" || !strings.Contains(err.Error(), "button send timeout") {
		t.Fatalf("error = %v, want timeout error", err)
	}
}

func TestRunWakeSequenceJigglesControlLines(t *testing.T) {
	port := &mockPort{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := RunWakeSequence(ctx, port, WakeSequence{Hold: time.Millisecond}); err != nil {
		t.Fatalf("RunWakeSequence error: %v", err)
	}
	if got, want := len(port.dtr), 3; got != want {
		t.Fatalf("DTR toggles = %d, want %d: %v", got, want, port.dtr)
	}
	if got, want := len(port.rts), 2; got != want {
		t.Fatalf("RTS toggles = %d, want %d: %v", got, want, port.rts)
	}
	if !port.dtr[0] || port.dtr[1] || !port.dtr[2] || !port.rts[0] || port.rts[1] {
		t.Fatalf("unexpected wake sequence DTR=%v RTS=%v", port.dtr, port.rts)
	}
}

func TestLocalWakeTransportUsesWakeSequence(t *testing.T) {
	port := &mockPort{}
	wake := NewLocalWakeTransport("/dev/ttyTEST0", &mockOpener{port: port}, time.Second)
	wake.sequence = WakeSequence{Hold: time.Millisecond}
	result, err := wake.SendWake(context.Background())
	if err != nil {
		t.Fatalf("SendWake error: %v", err)
	}
	if !result.Sent || result.Name != "wake" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(port.dtr) != 3 || len(port.rts) != 2 {
		t.Fatalf("wake did not toggle control lines: dtr=%v rts=%v", port.dtr, port.rts)
	}
}

func TestLocalWakeTransportRequiresPort(t *testing.T) {
	wake := NewLocalWakeTransport("", nil, time.Second)
	_, err := wake.SendWake(context.Background())
	actionErr := buttonActionError(err)
	if actionErr == nil || actionErr.StatusCode != 503 {
		t.Fatalf("error = %v, want 503 wake transport unavailable", err)
	}
}
