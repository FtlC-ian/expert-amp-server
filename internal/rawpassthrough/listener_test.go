package rawpassthrough

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
)

// fakePort is a serial.Port backed by in-memory pipes, so a test can act as the
// amplifier on the far end of the lease.
type fakePort struct {
	toHost   chan []byte
	fromHost chan []byte

	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

func newFakePort() *fakePort {
	return &fakePort{
		toHost:   make(chan []byte, 16),
		fromHost: make(chan []byte, 16),
		done:     make(chan struct{}),
	}
}

func (p *fakePort) Read(buf []byte) (int, error) {
	select {
	case chunk := <-p.toHost:
		return copy(buf, chunk), nil
	case <-p.done:
		return 0, errors.New("port closed")
	}
}

func (p *fakePort) Write(buf []byte) (int, error) {
	chunk := append([]byte(nil), buf...)
	select {
	case p.fromHost <- chunk:
		return len(buf), nil
	case <-p.done:
		return 0, errors.New("port closed")
	}
}

func (p *fakePort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	return nil
}

func (p *fakePort) SetReadTimeout(time.Duration) error { return nil }
func (p *fakePort) SetDTR(bool) error                  { return nil }
func (p *fakePort) SetRTS(bool) error                  { return nil }

type fakeOpener struct {
	mu    sync.Mutex
	ports []*fakePort
}

func (o *fakeOpener) Open(string, int) (serial.Port, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	port := newFakePort()
	o.ports = append(o.ports, port)
	return port, nil
}

func (o *fakeOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.ports)
}

// latest is the most recently opened port. While a passthrough session is live
// that is the leased one, because the internal read loop is stopped and cannot
// open another.
func (o *fakeOpener) latest() *fakePort {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ports) == 0 {
		return nil
	}
	return o.ports[len(o.ports)-1]
}

// waitForNewPort blocks until a port beyond baseline has been opened.
func (o *fakeOpener) waitForNewPort(t *testing.T, baseline int) *fakePort {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.count() > baseline {
			return o.latest()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("passthrough never leased a serial port")
	return nil
}

func newTestController(t *testing.T) (*Controller, *fakeOpener, func()) {
	t.Helper()
	opener := &fakeOpener{}
	src := runtime.NewSerialSource(runtime.SerialSourceConfig{
		Port:             "/dev/ttyTEST0",
		BaudRate:         115200,
		ReadTimeout:      10 * time.Millisecond,
		ReadSize:         512,
		MinFrameLen:      64,
		MaxBuffer:        8192,
		IOTimeout:        2 * time.Second,
		ReconnectBackoff: 50 * time.Millisecond,
	}, opener, runtime.Update{})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)

	controller := New(Config{Enabled: true, ListenAddress: "127.0.0.1:0", Source: src})
	if controller == nil {
		cancel()
		t.Fatal("expected a controller for an enabled config")
	}
	return controller, opener, cancel
}

// startListener runs the controller on an ephemeral port and returns the bound
// address, without dialing it (a probe connection would consume a lease).
func startListener(t *testing.T, ctx context.Context, c *Controller) string {
	t.Helper()
	go func() { _ = c.Start(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := c.Addr(); addr != "" {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener never bound")
	return ""
}

func TestPassthroughPipesBytesBothDirections(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	baseline := opener.count()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	port := opener.waitForNewPort(t, baseline)

	// client -> amplifier
	if _, err := conn.Write([]byte{0x55, 0x55, 0x55, 0x01, 0x90, 0x90}); err != nil {
		t.Fatalf("write to socket: %v", err)
	}
	select {
	case got := <-port.fromHost:
		if len(got) != 6 || got[4] != 0x90 {
			t.Fatalf("amplifier received %x, want the status poll verbatim", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bytes written by the client never reached the serial port")
	}

	// amplifier -> client
	reply := []byte{0xAA, 0xAA, 0xAA, 0x01, 0x0C, 0x0C}
	port.toHost <- reply
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len(reply))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read from socket: %v", err)
	}
	for i := range reply {
		if buf[i] != reply[i] {
			t.Fatalf("client received %x, want %x forwarded verbatim", buf, reply)
		}
	}
}

func TestPassthroughRejectsSecondConcurrentClient(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	baseline := opener.count()
	first, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer first.Close()

	opener.waitForNewPort(t, baseline)

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer second.Close()

	// The server closes a rejected client immediately rather than queueing it.
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := second.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected the second client to be closed immediately, got %v", err)
	}
}

// A session must be refused, not silently degraded, while a server-side
// automatic control is armed. The refusal has to name every control the
// operator must disarm.
func TestPassthroughRefusesWhileAutomaticControlsAreArmed(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()

	armed := []string{"automatic fan control", "overtemperature standby"}
	controller.cfg.ArmedAutomaticControls = func() []string { return armed }

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	baseline := opener.count()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected the client to be refused and closed, got %v", err)
	}

	// Refused before anything was taken: the serial port was never stolen.
	if got := opener.count(); got != baseline {
		t.Fatalf("serial port was opened for a refused session: count %d -> %d", baseline, got)
	}

	status := controller.Status()
	if len(status.BlockedByArmedControls) != len(armed) {
		t.Fatalf("status.BlockedByArmedControls = %v, want %v", status.BlockedByArmedControls, armed)
	}
	for _, want := range armed {
		if !strings.Contains(status.Note, want) {
			t.Fatalf("status note %q does not name %q", status.Note, want)
		}
	}
}

// The refusal carries 409: it conflicts with current server state and succeeds
// unchanged once that state is corrected.
func TestAutomaticControlsArmedErrorReports409AndNamesEachControl(t *testing.T) {
	err := ErrAutomaticControlsArmed([]string{"automatic fan control", "overtemperature standby"})

	var armedErr *AutomaticControlsArmedError
	if !errors.As(err, &armedErr) {
		t.Fatalf("expected an *AutomaticControlsArmedError, got %T", err)
	}
	if got := armedErr.HTTPStatus(); got != http.StatusConflict {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusConflict)
	}
	for _, want := range []string{"automatic fan control", "overtemperature standby"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err.Error(), want)
		}
	}
}

// With nothing armed the gate must stay out of the way entirely.
func TestPassthroughAcceptsWhenNoAutomaticControlsAreArmed(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()

	controller.cfg.ArmedAutomaticControls = func() []string { return nil }

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	baseline := opener.count()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	opener.waitForNewPort(t, baseline)

	if status := controller.Status(); !status.ClientConnected {
		t.Fatalf("expected the session to be accepted, got %+v", status)
	}
}

// automaticControlsAvailable reports whether the server owns the serial port.
// It read false in every state, including while the listener sat idle and the
// server held the port, which told an operator their automatic controls were
// unavailable when nothing was stopping them. All three states are pinned here
// because the field is only meaningful as a contrast between them.
func TestAutomaticControlsAvailableTracksSerialPortOwnership(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()

	// The accept loop reads this while the test changes what it reports, so the
	// value moves under a mutex and cfg itself is assigned once, before Start.
	var armedMu sync.Mutex
	var armedNow []string
	setArmed := func(controls []string) {
		armedMu.Lock()
		defer armedMu.Unlock()
		armedNow = controls
	}
	controller.cfg.ArmedAutomaticControls = func() []string {
		armedMu.Lock()
		defer armedMu.Unlock()
		return armedNow
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	// Idle: no client, nothing armed. The server owns the port and can act.
	setArmed(nil)
	status := controller.Status()
	if !status.AutomaticControlsAvailable {
		t.Fatalf("idle listener reports automatic controls unavailable: %+v", status)
	}
	if status.ClientConnected {
		t.Fatalf("expected no client connected while idle: %+v", status)
	}

	// Armed-idle: controls armed and no client. Still available -- armed controls
	// block the next client from taking the port, they do not stop the server
	// from using it. This is the state an operator checks before connecting.
	armed := []string{"automatic fan control", "overtemperature standby"}
	setArmed(armed)
	status = controller.Status()
	if !status.AutomaticControlsAvailable {
		t.Fatalf("armed-idle listener reports automatic controls unavailable: %+v", status)
	}
	if len(status.BlockedByArmedControls) != len(armed) {
		t.Fatalf("armed-idle blockedByArmedControls = %v, want %v", status.BlockedByArmedControls, armed)
	}

	// Connected: the lease is held, the server emits no bytes of its own, so its
	// automatic controls genuinely cannot act.
	setArmed(nil)
	baseline := opener.count()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	opener.waitForNewPort(t, baseline)

	status = controller.Status()
	if !status.ClientConnected {
		t.Fatalf("expected a connected client: %+v", status)
	}
	if status.AutomaticControlsAvailable {
		t.Fatalf("automatic controls reported available while a client holds the lease: %+v", status)
	}
}

func TestNewReturnsNilWhenDisabled(t *testing.T) {
	if c := New(Config{Enabled: false, ListenAddress: ":7388"}); c != nil {
		t.Fatal("expected nil controller when passthrough is disabled")
	}
	if c := New(Config{Enabled: true, ListenAddress: ""}); c != nil {
		t.Fatal("expected nil controller without a listen address")
	}
	if c := New(Config{Enabled: true, ListenAddress: ":7388", Source: nil}); c != nil {
		t.Fatal("expected nil controller without a serial source")
	}
}

// Arming an automatic control while a raw client holds the port produces a
// control that reports itself armed and cannot act: every serial write it would
// make is refused for the life of the lease. The transaction is told so, and
// refuses the arming instead.
func TestHoldForSettingsTransactionReportsAClientHoldingThePort(t *testing.T) {
	controller, _, cancel := newTestController(t)
	defer cancel()
	controller.cfg.ArmedAutomaticControls = func() []string { return nil }

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	// Idle: nothing holds the port, so an update is free to arm.
	ran := false
	if err := controller.HoldForSettingsTransaction(func(leased bool) error {
		ran = true
		if leased {
			t.Error("leased = true with no client connected")
		}
		return nil
	}); err != nil {
		t.Fatalf("idle transaction returned %v", err)
	}
	if !ran {
		t.Fatal("the commit never ran")
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	waitForLease(t, controller)

	// Leased: the commit is still run -- disarming and unrelated settings must
	// not be blocked -- but it is told the port is held, so it can refuse only
	// what actually conflicts.
	err = controller.HoldForSettingsTransaction(func(leased bool) error {
		if !leased {
			t.Error("leased = false while a raw client held the port")
		}
		return ErrArmingDuringLease([]string{"overtemperature standby"})
	})
	if err == nil {
		t.Fatal("the refusal did not reach the caller")
	}
	var leaseErr *ArmingDuringLeaseError
	if !errors.As(err, &leaseErr) {
		t.Fatalf("expected an *ArmingDuringLeaseError, got %T: %v", err, err)
	}
	if got := leaseErr.HTTPStatus(); got != http.StatusConflict {
		t.Fatalf("HTTPStatus() = %d, want %d", got, http.StatusConflict)
	}
	if !strings.Contains(err.Error(), "overtemperature standby") {
		t.Fatalf("error %q does not name the control it refused to arm", err.Error())
	}
}

// The decision has to be taken inside the same boundary that claims the port,
// and stay true for as long as the update takes to commit. While a transaction
// runs, session setup cannot -- which is what stops an update from writing
// "armed" into a session being established at the same moment.
func TestHoldForSettingsTransactionSerializesAgainstSessionSetup(t *testing.T) {
	controller, _, cancel := newTestController(t)
	defer cancel()
	controller.cfg.ArmedAutomaticControls = func() []string { return nil }

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)

	committing := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- controller.HoldForSettingsTransaction(func(leased bool) error {
			if leased {
				t.Error("leased = true before any client connected")
			}
			close(committing)
			<-release
			return nil
		})
	}()
	<-committing

	// A client arriving mid-transaction cannot take the port until it commits.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	settled := make(chan struct{})
	go func() {
		defer close(settled)
		for {
			controller.mu.Lock()
			leased := controller.handle != nil
			controller.mu.Unlock()
			if leased {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	select {
	case <-settled:
		t.Fatal("a session was established while a settings transaction held the setup boundary")
	case <-time.After(250 * time.Millisecond):
	}

	// Once the update commits, setup proceeds as normal.
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("transaction returned %v", err)
	}
	waitForLease(t, controller)
}

func waitForLease(t *testing.T, c *Controller) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		leased := c.handle != nil
		c.mu.Unlock()
		if leased {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no passthrough lease was established")
}

// A listener that never bound must not be reported as something a client could
// connect to. Enabled is configuration; ListenerAvailable is fact.
func TestStatusReportsTheListenerUnavailableWhenBindingFails(t *testing.T) {
	controller, _, cancel := newTestController(t)
	defer cancel()
	controller.cfg.ArmedAutomaticControls = func() []string { return nil }

	// Take the address first, then point the controller at it.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer occupied.Close()
	controller.cfg.ListenAddress = occupied.Addr().String()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := controller.Start(ctx); err == nil {
		t.Fatal("Start returned nil for an address already in use")
	}

	status := controller.Status()
	if status.ListenerAvailable {
		t.Fatal("listenerAvailable = true after the bind failed")
	}
	// "configured and running" is what enabled means; this one is not running.
	if status.Enabled {
		t.Fatal("enabled = true for a listener that never bound")
	}
	if !strings.Contains(status.Note, "listener is unavailable") {
		t.Fatalf("note %q does not report the listener as unavailable", status.Note)
	}
}

// The happy path still reports an available listener, so the flag cannot be
// satisfied by simply always reporting false.
func TestStatusReportsTheListenerAvailableOnceBound(t *testing.T) {
	controller, _, cancel := newTestController(t)
	defer cancel()
	controller.cfg.ArmedAutomaticControls = func() []string { return nil }

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	startListener(t, ctx, controller)

	if status := controller.Status(); !status.ListenerAvailable {
		t.Fatalf("listenerAvailable = false while bound and accepting: %+v", status)
	}
}
