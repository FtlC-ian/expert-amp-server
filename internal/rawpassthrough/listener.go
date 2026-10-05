// Package rawpassthrough exposes the amplifier's serial link over TCP so a
// single external client can drive it directly.
//
// The amplifier tolerates one serial master at a time, so this is an exclusive
// lease, not a multiplexer: while a client is connected the server's own
// polling is stopped and its writes are refused. Bytes are forwarded verbatim
// in both directions. The amplifier->client direction is additionally tapped
// (without altering it) so the API and UI keep showing telemetry, labelled
// passthrough-tap and treated as display evidence only.
//
// Server-side automatic control is not weakened during a lease, it is refused
// before one starts: a session cannot begin while automatic fan control or
// overtemperature standby is armed.
package rawpassthrough

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

const (
	// keepAlivePeriod reaps a client that has gone away without closing its
	// socket. There is deliberately no data-idle timeout: a legitimate client
	// may sit connected without polling.
	keepAlivePeriod = 30 * time.Second

	// copyBufferSize is a single serial read's worth of bytes. The display
	// frame this amplifier emits is 371 bytes.
	copyBufferSize = 4096

	// writeTimeout bounds a write to the client. A peer that stops reading but
	// keeps its socket open holds its receive window at zero; keepalive probes
	// are still answered, so the connection looks healthy while the forwarding
	// goroutine blocks forever. That would silently freeze the tap, and with it
	// overtemperature protection, for as long as the client stays wedged.
	writeTimeout = 10 * time.Second

	// acceptBackoff paces the accept loop after a temporary error such as fd
	// exhaustion, instead of spinning at full speed.
	acceptBackoff = 100 * time.Millisecond
)

// AutomaticControlsArmedError reports that a passthrough session was refused
// because server-side automatic controls are armed. Controls names every
// control the operator must disarm, so a caller never has to guess which one.
type AutomaticControlsArmedError struct {
	Controls []string
}

func (e *AutomaticControlsArmedError) Error() string {
	return fmt.Sprintf(
		"raw passthrough is unavailable while these automatic controls are armed: %s. "+
			"The server sends no serial bytes of its own during a passthrough lease, so they could not act. "+
			"Disarm them explicitly to use passthrough.",
		strings.Join(e.Controls, ", "),
	)
}

// HTTPStatus is 409: the request conflicts with current server state, and
// succeeds unchanged once that state is corrected.
func (e *AutomaticControlsArmedError) HTTPStatus() int { return http.StatusConflict }

// ErrAutomaticControlsArmed builds the refusal for the named armed controls.
func ErrAutomaticControlsArmed(controls []string) error {
	return &AutomaticControlsArmedError{Controls: controls}
}

// ArmingDuringLeaseError reports that an automatic control could not be armed
// because a raw passthrough client currently holds the serial port. Controls
// names what the rejected update would have armed.
type ArmingDuringLeaseError struct {
	Controls []string
}

func (e *ArmingDuringLeaseError) Error() string {
	return fmt.Sprintf(
		"cannot arm these automatic controls while a raw passthrough client holds the serial port: %s. "+
			"The server sends no serial bytes of its own during a passthrough lease, so they would read as armed "+
			"without being able to act. Disconnect the passthrough client first.",
		strings.Join(e.Controls, ", "),
	)
}

// HTTPStatus is 409: the request conflicts with current server state, and
// succeeds unchanged once the lease ends.
func (e *ArmingDuringLeaseError) HTTPStatus() int { return http.StatusConflict }

// ErrArmingDuringLease builds the refusal for controls an update would arm
// while a client holds the port.
func ErrArmingDuringLease(controls []string) error {
	return &ArmingDuringLeaseError{Controls: controls}
}

// HoldForSettingsTransaction runs commit inside the session-setup boundary, so
// that a settings update and the start of a passthrough session cannot
// interleave. leased reports whether a client holds the port, read inside that
// boundary and therefore still true when commit acts on it.
//
// Reading the answer somewhere else and passing it in would not be enough, and
// neither would reading it here and returning it. serve decides its own refusal
// and claims the port under setupMu, so an update that sampled "no client" and
// then went away to read, merge and commit could be overtaken by a session being
// established -- leaving controls that report themselves armed for a lease that
// blocks every write they would make. Holding the boundary across the whole
// commit is what removes the window: either serve claims the port first and
// commit sees leased true, or commit holds the boundary and serve blocks until
// the update is written, whereupon its existing armed-controls check refuses the
// client for the stated reason.
//
// Everything the update decides has to be read inside commit -- what the
// settings currently are, what merging the request produces, and whether that
// newly arms anything. A value captured before the call is exactly the stale
// snapshot this boundary exists to prevent, however carefully the boundary
// itself is held.
//
// Lock order, which is not obvious and cannot be inverted: callers may hold a
// mutex of their own above this, and commit may take config.Manager's lock
// below it, because serve already establishes setupMu -> config.Manager.mu when
// it reads the armed controls. Anything that held config.Manager's lock across
// this call would invert that pair and deadlock against a connecting client, so
// config reads and writes inside commit must be leaf calls that take and release
// that lock themselves.
//
// A nil controller means passthrough is not configured at all: there is no
// boundary to take and no lease to conflict with, so commit runs with leased
// false.
func (c *Controller) HoldForSettingsTransaction(commit func(leased bool) error) error {
	if c == nil {
		return commit(false)
	}
	c.setupMu.Lock()
	defer c.setupMu.Unlock()
	c.mu.Lock()
	leased := c.handle != nil
	c.mu.Unlock()
	return commit(leased)
}

// armedControls reports which server-side automatic controls are armed.
func (c *Controller) armedControls() []string {
	if c == nil || c.cfg.ArmedAutomaticControls == nil {
		return nil
	}
	return c.cfg.ArmedAutomaticControls()
}

// Lease is the subset of the actuation coordinator this package needs.
// Acquire returns nil when another owner holds actuation; a non-nil result is
// the identity of this session's acquisition and only it can release the
// reservation.
type Lease interface {
	Acquire() transport.ActuationLease
}

// Config configures a Controller.
type Config struct {
	Enabled       bool
	ListenAddress string
	Source        *runtime.SerialSource
	Lease         Lease

	// ArmedAutomaticControls names the server-side automatic controls that are
	// currently armed, or nil when none are. A passthrough session is refused
	// while it returns anything: the server sends no bytes of its own during a
	// lease, so those controls could not act, and an operator must disarm them
	// knowingly rather than have them silently suspended and restored.
	ArmedAutomaticControls func() []string
}

// Controller accepts raw TCP clients and leases them the serial port.
type Controller struct {
	cfg Config

	// setupMu serializes session setup so the lease and the port are claimed as
	// one step, and so a refusal is decided before either is taken. Since
	// v0.4.8 the coordinator itself also rejects a second concurrent
	// acquisition by the same owner name and scopes Release to the exact lease
	// instance, so this no longer guards a lost-lease bug — only the ordering
	// between the coordinator reservation and the serial port claim.
	setupMu sync.Mutex

	mu        sync.Mutex
	listener  net.Listener
	listenErr error
	conn      net.Conn
	handle    *runtime.RawPassthroughHandle
	since     time.Time
}

// New builds a Controller. It returns nil when passthrough is not usable, so
// callers can treat "disabled" and "no serial port" identically.
func New(cfg Config) *Controller {
	if !cfg.Enabled || cfg.Source == nil || cfg.ListenAddress == "" {
		return nil
	}
	return &Controller{cfg: cfg}
}

// Start listens and serves until ctx is canceled. It blocks.
func (c *Controller) Start(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", c.cfg.ListenAddress)
	if err != nil {
		// Remember why, so /api/v1/raw-passthrough can say the listener is not
		// there instead of reporting a passthrough that is enabled in
		// configuration and unreachable in fact.
		c.mu.Lock()
		c.listenErr = err
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.listener = listener
	c.listenErr = nil
	c.mu.Unlock()

	log.Printf("raw serial passthrough listening on %s (serial port is leased exclusively while a client is connected)", c.cfg.ListenAddress)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
		// Drop any live session so the port is returned on shutdown. A readLoop
		// parked on lifecycleMu cannot observe cancellation on its own.
		c.closeActiveConn()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Back off rather than spin: under fd exhaustion Accept returns
			// immediately and a bare continue would flood the log at full speed.
			log.Printf("raw passthrough accept failed: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(acceptBackoff):
			}
			continue
		}
		go c.serve(ctx, conn)
	}
}

func (c *Controller) serve(ctx context.Context, conn net.Conn) {
	remote := conn.RemoteAddr().String()

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(keepAlivePeriod)
	}

	// Claim the lease and the port as one atomic step; see setupMu.
	c.setupMu.Lock()

	c.mu.Lock()
	busy := c.handle != nil
	c.mu.Unlock()
	if busy {
		c.setupMu.Unlock()
		log.Printf("raw passthrough refused %s: %v", remote, runtime.ErrRawPassthroughBusy)
		_ = conn.Close()
		return
	}

	// Refuse before taking anything while a server-side automatic control is
	// armed. Suspending them for the session would mean tracking what was
	// suspended and restoring it across disconnects, crashes, and failed port
	// reclamation; refusing keeps the state simple and honest.
	if blocked := c.armedControls(); len(blocked) > 0 {
		c.setupMu.Unlock()
		log.Printf("raw passthrough refused %s: %s", remote, ErrAutomaticControlsArmed(blocked).Error())
		_ = conn.Close()
		return
	}

	// Refuse rather than interrupt an automatic transaction that is already
	// driving the amplifier. Matches the coordinator's reject-don't-queue rule.
	var lease transport.ActuationLease
	if c.cfg.Lease != nil {
		if lease = c.cfg.Lease.Acquire(); lease == nil {
			c.setupMu.Unlock()
			log.Printf("raw passthrough refused %s: %s", remote, transport.ActuationBusyError("").Error())
			_ = conn.Close()
			return
		}
	}

	handle, err := c.cfg.Source.BeginRawPassthrough(ctx)
	if err != nil {
		if lease != nil {
			lease.Release()
		}
		c.setupMu.Unlock()
		log.Printf("raw passthrough refused %s: %v", remote, err)
		_ = conn.Close()
		return
	}

	c.mu.Lock()
	c.conn = conn
	c.handle = handle
	c.since = time.Now()
	c.mu.Unlock()
	c.setupMu.Unlock()

	log.Printf("raw passthrough client %s connected; server polling paused and no server-side automatic control is available until it disconnects", remote)

	var once sync.Once
	done := make(chan struct{})
	finish := func() {
		once.Do(func() {
			close(done)
			_ = conn.Close()
			handle.Close()
			// Releasing this exact lease is a no-op unless it is still the
			// coordinator's current reservation, so a late teardown cannot
			// clear a newer session's ownership.
			if lease != nil {
				lease.Release()
			}
			c.mu.Lock()
			// Compare before clearing: a reconnecting client can have already
			// installed its own session by the time this teardown runs, and
			// blindly nulling would report "no client connected" while that
			// session is live and would leave it unreachable on shutdown.
			if c.handle == handle {
				c.conn = nil
				c.handle = nil
			}
			c.mu.Unlock()
		})
	}
	defer finish()

	// serve must watch ctx itself. The listener's shutdown watcher takes a
	// one-shot snapshot of the active connection, so a session that is still
	// being established when cancellation lands would otherwise never be torn
	// down, leaking the lifecycle lock and leaving the port with the client.
	go func() {
		select {
		case <-ctx.Done():
			log.Printf("raw passthrough disconnecting %s: server is shutting down", remote)
			finish()
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// client -> amplifier, forwarded verbatim.
	go func() {
		defer wg.Done()
		defer finish()
		_, _ = io.Copy(handle.Port(), conn)
	}()

	// amplifier -> client, forwarded verbatim and tapped.
	go func() {
		defer wg.Done()
		defer finish()
		buf := make([]byte, copyBufferSize)
		port := handle.Port()
		for {
			select {
			case <-done:
				return
			default:
			}

			n, err := port.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				if _, werr := conn.Write(chunk); werr != nil {
					log.Printf("raw passthrough write to client failed, ending session: %v", werr)
					return
				}
				// Observe after forwarding so the client is never delayed by
				// decoding, and never affected by it. Both stream decoders copy
				// out of chunk, so reusing buf across iterations is safe.
				handle.ObserveFromAmp(chunk)
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return
			}
		}
	}()

	wg.Wait()
	log.Printf("raw passthrough client %s disconnected; server polling resumed", remote)
}

// Addr is the address the listener actually bound to, or "" before Start binds.
func (c *Controller) Addr() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listener == nil {
		return ""
	}
	return c.listener.Addr().String()
}

func (c *Controller) closeActiveConn() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Status describes the passthrough listener for the diagnostics API.
type Status struct {
	Enabled               bool   `json:"enabled"`
	ListenAddress         string `json:"listenAddress,omitempty"`
	ClientConnected       bool   `json:"clientConnected"`
	ClientAddress         string `json:"clientAddress,omitempty"`
	ConnectedSince        string `json:"connectedSince,omitempty"`
	StatusFramesObserved  int64  `json:"statusFramesObserved,omitempty"`
	DisplayFramesObserved int64  `json:"displayFramesObserved,omitempty"`

	// TapFresh reports whether tapped telemetry is currently being refreshed by
	// the connected client. It is display freshness only and never implies that
	// any server-side automatic control is available.
	TapFresh bool `json:"tapFresh"`

	// BlockedByArmedControls lists controls that must be disarmed before a
	// session can start. Non-empty means a new client will be refused.
	BlockedByArmedControls []string `json:"blockedByArmedControls,omitempty"`

	// AutomaticControlsAvailable reports whether the server owns the serial port
	// and could therefore act on it. It is false for the lifetime of a lease,
	// because the server emits no bytes of its own while a raw client holds the
	// port, and true whenever no client is connected.
	//
	// It describes port ownership, not arming: true does not say that
	// overtemperature standby or automatic fan control is enabled or armed, only
	// that nothing is stopping them from acting. BlockedByArmedControls is what
	// reports which controls are armed right now.
	AutomaticControlsAvailable bool `json:"automaticControlsAvailable"`

	// ListenerAvailable reports whether the TCP listener is actually accepting.
	//
	// It does not disagree with Enabled when binding fails: Enabled means
	// configured *and* running, so a listener that never bound sets both false.
	// What this field adds is which of those two it was -- false here alongside
	// a bind error in Note is a passthrough that could not start, where both
	// false and no error is one the operator turned off. The only state where
	// the two genuinely differ is a controller that has been constructed but has
	// not started listening yet.
	ListenerAvailable bool `json:"listenerAvailable"`

	Note string `json:"note,omitempty"`
}

// Status snapshots the listener state.
func (c *Controller) Status() Status {
	if c == nil {
		return Status{Enabled: false}
	}
	c.mu.Lock()
	conn := c.conn
	handle := c.handle
	since := c.since
	listener := c.listener
	listenErr := c.listenErr
	c.mu.Unlock()

	out := Status{Enabled: true, ListenAddress: c.cfg.ListenAddress, ListenerAvailable: listener != nil}
	if listenErr != nil {
		// Enabled is documented as "configured and running", so a listener that
		// never bound is not enabled however the configuration reads. Reporting
		// it true here described a passthrough no client could reach.
		// listenerAvailable and the note are what separate this from a
		// passthrough the operator simply turned off.
		out.Enabled = false
		out.AutomaticControlsAvailable = true
		out.BlockedByArmedControls = c.armedControls()
		out.Note = fmt.Sprintf(
			"raw serial passthrough is enabled but its listener is unavailable: %v. "+
				"No client can connect, and the server owns the serial port.", listenErr)
		return out
	}
	if conn == nil || handle == nil {
		// Idle, armed or not: the server still owns the port, so its automatic
		// controls can act. Armed controls block the next client from taking the
		// port; they do not stop the server from using it.
		out.AutomaticControlsAvailable = true
		out.BlockedByArmedControls = c.armedControls()
		if len(out.BlockedByArmedControls) > 0 {
			out.Note = ErrAutomaticControlsArmed(out.BlockedByArmedControls).Error()
			return out
		}
		out.Note = "no raw client connected; the server owns the serial port"
		return out
	}
	stats := handle.Stats()
	out.ClientConnected = true
	out.ClientAddress = conn.RemoteAddr().String()
	out.ConnectedSince = since.UTC().Format(time.RFC3339)
	out.StatusFramesObserved = stats.StatusFramesSeen
	out.DisplayFramesObserved = stats.DisplayFrames
	out.TapFresh = handle.TapIsFresh()
	out.Note = "a raw client holds the serial port; server writes are refused and no server-side automatic control is available for this session. " +
		"Status shown from tapped traffic is labelled passthrough-tap and is display evidence only."
	return out
}
