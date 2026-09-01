package transport

import (
	"context"
	"fmt"
	"sync"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

const (
	ActuationOwnerFan       = "fan-policy"
	ActuationOwnerMenuDebug = "menu-debug"
	ActuationOwnerSafety    = "overtemperature-safety"
)

// LeaseButtonTransport is an owner-scoped view of an ActuationCoordinator.
// Safety ownership may preempt fan ownership; neither automatic owner can
// interleave with manual API writes.
type LeaseButtonTransport interface {
	ButtonTransport
	Acquire() ActuationLease
	SafetyHold() bool
}

// ActuationLease is the identity of one successful coordinator acquisition.
// Release is idempotent and affects the coordinator only while this exact
// lease remains current.
type ActuationLease interface {
	Release()
}

type ActuationCoordinator struct {
	mu        sync.Mutex
	transport ButtonTransport
	lease     *actuationLease
	wakeDone  chan struct{}
}

type ownedButtonTransport struct {
	coordinator *ActuationCoordinator
	owner       string
	safety      bool
}

type gatedWakeTransport struct {
	coordinator *ActuationCoordinator
	transport   WakeTransport
}

type actuationLease struct {
	coordinator *ActuationCoordinator
	owner       *ownedButtonTransport
	released    bool
}

func NewActuationCoordinator(buttonTransport ButtonTransport) *ActuationCoordinator {
	return &ActuationCoordinator{transport: buttonTransport}
}

func (c *ActuationCoordinator) Owner(owner string, safety bool) LeaseButtonTransport {
	return &ownedButtonTransport{coordinator: c, owner: owner, safety: safety}
}

// GateWake rejects the separate DTR/RTS wake path while an automatic
// transaction owns the actuator. Wake can change amplifier state and disrupt
// the live serial handle, so it shares the same exclusivity boundary.
func (c *ActuationCoordinator) GateWake(wakeTransport WakeTransport) WakeTransport {
	if c == nil || wakeTransport == nil {
		return wakeTransport
	}
	return &gatedWakeTransport{coordinator: c, transport: wakeTransport}
}

// SendButton is the manual/API path. It is rejected while any automatic
// transaction owns the actuator.
func (c *ActuationCoordinator) SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error) {
	if c == nil || c.transport == nil {
		return api.ActionResult{Name: action.Name}, TransportUnavailableError()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease != nil || c.wakeDone != nil {
		return api.ActionResult{Name: action.Name}, ActuationBusyError(c.busyOwnerLocked())
	}
	return c.transport.SendButton(ctx, action)
}

func (t *ownedButtonTransport) Acquire() ActuationLease {
	if t == nil || t.coordinator == nil {
		return nil
	}
	c := t.coordinator
	if t.safety {
		c.lockAfterWake()
	} else {
		c.mu.Lock()
	}
	defer c.mu.Unlock()
	if t.safety {
		if c.lease != nil && c.lease.owner.safety {
			return nil
		}
		lease := &actuationLease{coordinator: c, owner: t}
		c.lease = lease
		return lease
	}
	if c.wakeDone != nil || c.lease != nil {
		return nil
	}
	lease := &actuationLease{coordinator: c, owner: t}
	c.lease = lease
	return lease
}

func (l *actuationLease) Release() {
	if l == nil || l.coordinator == nil {
		return
	}
	c := l.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	if c.lease == l {
		c.lease = nil
	}
}

func (t *ownedButtonTransport) SafetyHold() bool {
	if t == nil || t.coordinator == nil {
		return false
	}
	t.coordinator.mu.Lock()
	defer t.coordinator.mu.Unlock()
	return t.coordinator.lease != nil && t.coordinator.lease.owner.safety
}

func (t *ownedButtonTransport) SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error) {
	if t == nil || t.coordinator == nil || t.coordinator.transport == nil {
		return api.ActionResult{Name: action.Name}, TransportUnavailableError()
	}
	c := t.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease == nil || c.lease.owner != t {
		return api.ActionResult{Name: action.Name}, ActuationBusyError(c.busyOwnerLocked())
	}
	return c.transport.SendButton(ctx, action)
}

func (t *ownedButtonTransport) SendButtonForSerialSession(ctx context.Context, action api.ButtonAction, authorization SerialSessionWriteAuthorization) (api.ActionResult, error) {
	if t == nil || t.coordinator == nil || t.coordinator.transport == nil {
		return api.ActionResult{Name: action.Name}, TransportUnavailableError()
	}
	c := t.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease == nil || c.lease.owner != t {
		return api.ActionResult{Name: action.Name}, ActuationBusyError(c.busyOwnerLocked())
	}
	transport, ok := c.transport.(SerialSessionButtonTransport)
	if !ok {
		return api.ActionResult{Name: action.Name}, TransportUnavailableError()
	}
	return transport.SendButtonForSerialSession(ctx, action, authorization)
}

func (t *ownedButtonTransport) SerialSessionWritesAvailable() bool {
	if t == nil || t.coordinator == nil || t.coordinator.transport == nil {
		return false
	}
	_, ok := t.coordinator.transport.(SerialSessionButtonTransport)
	return ok
}

func (t *gatedWakeTransport) SendWake(ctx context.Context) (api.ActionResult, error) {
	if t == nil || t.coordinator == nil || t.transport == nil {
		return api.ActionResult{Name: "wake"}, WakeTransportUnavailableError()
	}
	c := t.coordinator
	c.mu.Lock()
	if c.lease != nil || c.wakeDone != nil {
		owner := c.busyOwnerLocked()
		c.mu.Unlock()
		return api.ActionResult{Name: "wake"}, ActuationBusyError(owner)
	}
	done := make(chan struct{})
	c.wakeDone = done
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.wakeDone == done {
			c.wakeDone = nil
			close(done)
		}
		c.mu.Unlock()
	}()
	return t.transport.SendWake(ctx)
}

// lockAfterWake preserves safety preemption semantics without holding the
// coordinator mutex across wake I/O. The safety controller historically
// waited for an in-flight wake because SendWake held mu for the full call.
func (c *ActuationCoordinator) lockAfterWake() {
	for {
		c.mu.Lock()
		if c.wakeDone == nil {
			return
		}
		done := c.wakeDone
		c.mu.Unlock()
		<-done
	}
}

func (c *ActuationCoordinator) busyOwnerLocked() string {
	if c.wakeDone != nil {
		return "wake"
	}
	if c.lease != nil {
		return c.lease.owner.owner
	}
	return ""
}

func ActuationBusyError(owner string) *ButtonActionError {
	if owner == "" {
		owner = "automatic safety control"
	}
	return &ButtonActionError{
		StatusCode: 409,
		Message:    fmt.Sprintf("button actuation is reserved by %s", owner),
	}
}
