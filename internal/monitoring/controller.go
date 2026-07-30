package monitoring

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

const (
	ActionIdle        = "idle"
	ActionPending     = "pending"
	ActionSent        = "sent"
	ActionUnconfirmed = "unconfirmed"
	ActionFailed      = "failed"
)

type ActionStatus struct {
	State       string `json:"state"`
	Name        string `json:"name,omitempty"`
	RequestedAt string `json:"requestedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Error       string `json:"error,omitempty"`
	Observation string `json:"observation,omitempty"`
}

type ControlSettings struct {
	Enabled    bool
	Armed      bool
	Thresholds Thresholds
}

type safetyLeaseButtonTransport interface {
	transport.ButtonTransport
	Acquire() bool
	Release()
	SetSafetyHold(bool)
}

// Controller performs the one deliberately narrow safety action supported by
// the server: one documented OPERATE command per overtemperature excursion to
// move an amplifier from OPERATE to STANDBY. It acts only when a fresh
// protocol-native status poll explicitly says OPERATE and RX.
//
// The latch is set before transport I/O and a failed or ambiguous write is never
// retried automatically. The toggle protocol has no request/response
// correlation, so the server never claims the resulting state is confirmed.
type Controller struct {
	mu        sync.RWMutex
	transport transport.ButtonTransport
	now       func() time.Time
	latched   bool
	action    ActionStatus
}

func NewController(buttonTransport transport.ButtonTransport) *Controller {
	return &Controller{
		transport: buttonTransport,
		now:       func() time.Time { return time.Now().UTC() },
		action:    ActionStatus{State: ActionIdle},
	}
}

func (c *Controller) Observe(ctx context.Context, status api.Status, settings ControlSettings) {
	if c == nil {
		return
	}
	result := Evaluate(status, settings.Enabled, settings.Thresholds)
	temperature := result.Observations.MaximumTemperatureC
	reset := effectiveResetThreshold(settings.Thresholds)

	c.mu.Lock()
	if c.latched && c.action.State == ActionSent {
		c.action.State = ActionUnconfirmed
		c.action.Observation = "standby command was sent once; the toggle protocol does not provide request/response correlation or command acknowledgement"
	}
	if !settings.Enabled || !settings.Armed {
		c.releaseSafetyHoldLocked()
	}
	if c.latched && temperature != nil && reset > 0 && *temperature < reset {
		c.latched = false
		c.releaseSafetyHoldLocked()
	}
	if c.latched ||
		!settings.Enabled ||
		!settings.Armed ||
		status.Provenance != "status-poll" ||
		!status.RecentContact ||
		!strings.EqualFold(strings.TrimSpace(status.OperatingState), "operate") ||
		status.TX == nil ||
		*status.TX ||
		temperature == nil ||
		settings.Thresholds.TemperatureTripC <= 0 ||
		*temperature < settings.Thresholds.TemperatureTripC {
		c.mu.Unlock()
		return
	}

	// Latch before attempting the toggle. This is the key no-retry guarantee.
	c.latched = true
	if leased, ok := c.transport.(safetyLeaseButtonTransport); ok {
		// Safety ownership preempts any lower-priority automatic transaction
		// and remains held for the full overtemperature excursion.
		leased.Acquire()
		leased.SetSafetyHold(true)
	}
	c.action = ActionStatus{
		State:       ActionPending,
		Name:        "standby",
		RequestedAt: c.now().Format(time.RFC3339Nano),
	}
	c.mu.Unlock()

	_, err := c.sendOnce(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.action.CompletedAt = c.now().Format(time.RFC3339Nano)
	if err != nil {
		c.action.State = ActionFailed
		c.action.Error = err.Error()
		c.releaseSafetyHoldLocked()
		return
	}
	c.action.State = ActionSent
	c.action.Error = ""
}

func (c *Controller) releaseSafetyHoldLocked() {
	if leased, ok := c.transport.(safetyLeaseButtonTransport); ok {
		leased.SetSafetyHold(false)
		leased.Release()
	}
}

func (c *Controller) sendOnce(ctx context.Context) (api.ActionResult, error) {
	if c.transport == nil {
		return api.ActionResult{}, transport.TransportUnavailableError()
	}
	writeCtx, cancel := context.WithTimeout(ctx, transport.DefaultButtonTimeout)
	defer cancel()
	return c.transport.SendButton(writeCtx, api.ButtonAction{Name: "operate"})
}

func (c *Controller) Apply(result Result, armed bool) Result {
	result.Armed = armed
	result.DryRun = !armed
	if c == nil {
		result.Action = ActionStatus{State: ActionIdle}
		return result
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result.Latched = c.latched
	result.Action = c.action
	if result.Action.State == "" {
		result.Action.State = ActionIdle
	}
	if result.Action.State == ActionSent || result.Action.State == ActionUnconfirmed {
		result.ActionsTaken = append(result.ActionsTaken, "OPERATE command sent once to request STANDBY")
	}
	return result
}

func (c *Controller) ObserveContactLoss() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.latched || (c.action.State != ActionSent && c.action.State != ActionUnconfirmed) {
		return
	}
	c.action.State = ActionUnconfirmed
	c.action.Observation = "amplifier contact was lost after the standby command; the transition is not confirmed"
}

func effectiveResetThreshold(thresholds Thresholds) float64 {
	if thresholds.TemperatureResetC > 0 {
		return thresholds.TemperatureResetC
	}
	return thresholds.TemperatureWarningC
}
