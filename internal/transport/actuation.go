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
	Acquire() bool
	Release()
	SafetyHold() bool
	SetSafetyHold(bool)
}

type ActuationCoordinator struct {
	mu         sync.Mutex
	transport  ButtonTransport
	owner      string
	safetyHold bool
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
	if c.owner != "" || c.safetyHold {
		return api.ActionResult{Name: action.Name}, ActuationBusyError(c.owner)
	}
	return c.transport.SendButton(ctx, action)
}

func (t *ownedButtonTransport) Acquire() bool {
	if t == nil || t.coordinator == nil {
		return false
	}
	c := t.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.safety {
		c.safetyHold = true
		c.owner = t.owner
		return true
	}
	if c.safetyHold || (c.owner != "" && c.owner != t.owner) {
		return false
	}
	c.owner = t.owner
	return true
}

func (t *ownedButtonTransport) Release() {
	if t == nil || t.coordinator == nil {
		return
	}
	c := t.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owner == t.owner {
		c.owner = ""
	}
}

func (t *ownedButtonTransport) SafetyHold() bool {
	if t == nil || t.coordinator == nil {
		return false
	}
	t.coordinator.mu.Lock()
	defer t.coordinator.mu.Unlock()
	return t.coordinator.safetyHold
}

func (t *ownedButtonTransport) SetSafetyHold(active bool) {
	if t == nil || t.coordinator == nil || !t.safety {
		return
	}
	c := t.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	c.safetyHold = active
	if active {
		c.owner = t.owner
	} else if c.owner == t.owner {
		c.owner = ""
	}
}

func (t *ownedButtonTransport) SendButton(ctx context.Context, action api.ButtonAction) (api.ActionResult, error) {
	if t == nil || t.coordinator == nil || t.coordinator.transport == nil {
		return api.ActionResult{Name: action.Name}, TransportUnavailableError()
	}
	c := t.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owner != t.owner || (!t.safety && c.safetyHold) {
		return api.ActionResult{Name: action.Name}, ActuationBusyError(c.owner)
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
	if c.owner != t.owner || (!t.safety && c.safetyHold) {
		return api.ActionResult{Name: action.Name}, ActuationBusyError(c.owner)
	}
	transport, ok := c.transport.(SerialSessionButtonTransport)
	if !ok {
		return api.ActionResult{Name: action.Name}, TransportUnavailableError()
	}
	return transport.SendButtonForSerialSession(ctx, action, authorization)
}

func (t *gatedWakeTransport) SendWake(ctx context.Context) (api.ActionResult, error) {
	if t == nil || t.coordinator == nil || t.transport == nil {
		return api.ActionResult{Name: "wake"}, WakeTransportUnavailableError()
	}
	t.coordinator.mu.Lock()
	defer t.coordinator.mu.Unlock()
	if t.coordinator.owner != "" || t.coordinator.safetyHold {
		return api.ActionResult{Name: "wake"}, ActuationBusyError(t.coordinator.owner)
	}
	return t.transport.SendWake(ctx)
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
