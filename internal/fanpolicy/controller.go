package fanpolicy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

const (
	navigationTimeout     = 4 * time.Second
	statusContactWindow   = 5 * time.Second
	normalRestoreCooldown = 15 * time.Minute
)

type ButtonTransport interface {
	SendButton(context.Context, api.ButtonAction) (api.ActionResult, error)
}

type leaseButtonTransport interface {
	ButtonTransport
	Acquire() bool
	Release()
	SafetyHold() bool
}

type DisplayObservation struct {
	State      display.State
	Generation uint64
	// TX is populated only from a checksum-valid LCD flag word. Nil means the
	// display frame cannot prove RX or TX and must not authorize a menu write.
	TX *bool
	// Operate is populated from the same checksum-valid LCD flag word. Nil
	// cannot prove either OPERATE or STANDBY.
	Operate                 *bool
	SerialSessionGeneration uint64
}

type navState struct {
	active                  bool
	failed                  bool
	model                   string
	serialSessionGeneration uint64
	step                    string
	target                  string
	observedPolicy          string
	afterGeneration         uint64
	previousKey             string
	deadline                time.Time
	lastAction              string
	lastError               string
	actions                 []string
	paused                  bool
	pauseReason             string
	pauseStatusGen          uint64
	rxStatusGen             uint64
	resumeAfterGen          uint64
	resumeDeadline          time.Time
	afterStatusGen          uint64
	restoreOperate          bool
	changedOperate          bool
	controlUncertain        bool
	leaseHeld               bool
	verifyOnly              bool
	recoveryAttempted       bool
	recoveryVerified        bool
	failureAfterGen         uint64
	profile                 string
	setupIndex              int
	expectedThirdSelection  string
}

type passiveSaveState struct {
	candidatePolicy string
	sawSave         bool
	sawStoring      bool
	thirdSeries     bool
	phase           string
	model           string
	firmware        string
	serialSession   uint64
	lastGeneration  uint64
}

type PersistentState struct {
	ManualOverride                string
	ManualOverrideDurationMinutes int
	ManualOverrideUntil           string
	LastVerifiedPolicy            string
	LastVerifiedAt                string
	LastVerifiedSource            string
	LastVerifiedModel             string
}

type StatePersistence func(PersistentState) error

type Controller struct {
	mu           sync.RWMutex
	transport    ButtonTransport
	desired      string
	current      string
	status       api.Status
	statusAt     time.Time
	baseSettings Settings
	settings     Settings
	result       Result
	nav          navState
	now          func() time.Time

	currentVerifiedAt             time.Time
	currentConfidence             string
	currentSource                 string
	currentModel                  string
	serialSessionGeneration       uint64
	statusSerialSessionGeneration uint64
	lastVerifiedHighAt            time.Time
	normalRestoreAfter            time.Time
	lastVerifiedScreen            string
	mayBeInMenu                   bool
	displayTX                     *bool
	displayOperate                *bool
	statusGeneration              uint64
	lastDisplayGen                uint64
	lastDisplayKey                string
	lastDisplayState              display.State
	manualOverride                string
	manualOverrideDuration        time.Duration
	manualOverrideUntil           time.Time
	verifyRequested               bool
	verifyReason                  string
	passive                       passiveSaveState
	persist                       StatePersistence
	persistenceError              string
}

func NewController(transports ...ButtonTransport) *Controller {
	var buttonTransport ButtonTransport
	if len(transports) != 0 {
		buttonTransport = transports[0]
	}
	now := time.Now()
	return &Controller{
		transport:         buttonTransport,
		desired:           PolicyUnknown,
		current:           PolicyUnknown,
		manualOverride:    PolicyUnknown,
		currentConfidence: "unknown",
		now:               time.Now,
		// A restart loses the in-memory verified-save receipt. Conservatively
		// hold a Normal request while current policy is unknown; high cooling
		// is never delayed.
		normalRestoreAfter: now.Add(normalRestoreCooldown),
		result: Result{
			State:         StateDisabled,
			DesiredPolicy: PolicyUnknown,
			CurrentPolicy: PolicyUnknown,
			BlockedBy:     []string{},
			Navigation:    Navigation{State: "idle", ActionsTaken: []string{}},
		},
	}
}

func (c *Controller) ConfigurePersistence(initial PersistentState, verifyOnStartup bool, persist StatePersistence) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.persist = persist
	c.manualOverride = normalizePolicy(initial.ManualOverride)
	if initial.ManualOverrideDurationMinutes > 0 {
		c.manualOverrideDuration = time.Duration(initial.ManualOverrideDurationMinutes) * time.Minute
	}
	if parsed, err := time.Parse(time.RFC3339, initial.ManualOverrideUntil); err == nil {
		c.manualOverrideUntil = parsed
	}
	if !c.manualOverrideUntil.IsZero() && !c.now().Before(c.manualOverrideUntil) {
		c.manualOverride = PolicyUnknown
		c.manualOverrideDuration = 0
		c.manualOverrideUntil = time.Time{}
	}
	if policy := normalizePolicy(initial.LastVerifiedPolicy); policy != PolicyUnknown {
		if verifiedAt, err := time.Parse(time.RFC3339, initial.LastVerifiedAt); err == nil {
			c.current = policy
			c.currentVerifiedAt = verifiedAt
			c.currentConfidence = "persisted-stale"
			c.currentSource = strings.TrimSpace(initial.LastVerifiedSource)
			c.currentModel = strings.TrimSpace(initial.LastVerifiedModel)
		}
	}
	if verifyOnStartup {
		c.verifyRequested = true
		c.verifyReason = "startup"
	}
	c.settings = c.effectiveSettingsLocked(c.baseSettings)
	c.persistLocked()
	c.result = c.decorateLocked(Evaluate(c.currentStatusLocked(c.now()), c.settings, c.desired), c.settings)
}

func (c *Controller) SetManualOverride(policy string, duration time.Duration) error {
	if c == nil {
		return fmt.Errorf("fan-policy controller unavailable")
	}
	normalized := normalizePolicy(policy)
	if strings.TrimSpace(policy) != "" && normalized == PolicyUnknown {
		return fmt.Errorf("manual fan override must be normal, high-cooling, or automatic")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nav.active {
		return fmt.Errorf("fan-policy navigation is already in progress")
	}
	if c.nav.failed {
		return fmt.Errorf("fan-policy navigation is failed closed; clear the failure through the documented recovery flow first")
	}
	if duration < 0 {
		return fmt.Errorf("manual fan override duration must not be negative")
	}
	c.manualOverride = normalized
	c.manualOverrideDuration = duration
	c.manualOverrideUntil = time.Time{}
	if normalized != PolicyUnknown {
		c.verifyRequested = false
		c.verifyReason = ""
	}
	c.desired = PolicyUnknown
	if c.nav.step == "complete" {
		c.nav = navState{}
	}
	c.settings = c.effectiveSettingsLocked(c.baseSettings)
	c.persistLocked()
	c.result = c.decorateLocked(Evaluate(c.currentStatusLocked(c.now()), c.settings, c.desired), c.settings)
	return nil
}

// ClearCompletedNormalOverride removes an inert manual Normal request so a
// separately guarded menu-debug session can take ownership. It never sends an
// amplifier command and refuses to clear high cooling or uncertain state.
func (c *Controller) ClearCompletedNormalOverride(status api.Status, settings Settings) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("fan-policy controller unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if settings.Enabled || c.manualOverride != PolicyNormal {
		return false, nil
	}
	if !status.RecentContact || status.TX == nil || *status.TX || normalizeOperatingState(status.OperatingState) != "standby" {
		return false, nil
	}
	if c.lastDisplayKey != "home" || c.lastDisplayGen == 0 || c.displayTX == nil || *c.displayTX || c.displayOperate == nil || *c.displayOperate {
		return false, nil
	}
	if c.nav.active || c.nav.failed || c.nav.paused || c.nav.leaseHeld || c.mayBeInMenu || c.verifyRequested {
		return false, nil
	}
	if c.current != PolicyNormal || c.currentConfidence != "verified-live" || c.currentVerifiedAt.IsZero() {
		return false, nil
	}

	previousOverride := c.manualOverride
	previousDuration := c.manualOverrideDuration
	previousUntil := c.manualOverrideUntil
	previousDesired := c.desired
	previousBaseSettings := c.baseSettings
	previousSettings := c.settings
	c.manualOverride = PolicyUnknown
	c.manualOverrideDuration = 0
	c.manualOverrideUntil = time.Time{}
	c.desired = PolicyUnknown
	c.baseSettings = settings
	c.settings = c.effectiveSettingsLocked(settings)
	c.persistLocked()
	if c.persistenceError != "" {
		persistErr := c.persistenceError
		c.manualOverride = previousOverride
		c.manualOverrideDuration = previousDuration
		c.manualOverrideUntil = previousUntil
		c.desired = previousDesired
		c.baseSettings = previousBaseSettings
		c.settings = previousSettings
		c.result = c.decorateLocked(Evaluate(status, c.settings, c.desired), c.settings)
		return false, fmt.Errorf("persist cleared Normal fan override: %s", persistErr)
	}
	if c.nav.step == "complete" {
		c.nav = navState{}
	}
	c.result = c.decorateLocked(Evaluate(status, c.settings, PolicyUnknown), c.settings)
	return true, nil
}

func (c *Controller) RequestVerification(reason string) error {
	if c == nil {
		return fmt.Errorf("fan-policy controller unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nav.active {
		return fmt.Errorf("fan-policy navigation is already in progress")
	}
	if c.nav.failed {
		return fmt.Errorf("fan-policy navigation is failed closed; clear the failure through the documented recovery flow first")
	}
	c.verifyRequested = true
	c.verifyReason = strings.TrimSpace(reason)
	if c.verifyReason == "" {
		c.verifyReason = "api"
	}
	c.desired = PolicyUnknown
	if c.nav.step == "complete" {
		c.nav = navState{}
	}
	c.settings = c.effectiveSettingsLocked(c.baseSettings)
	c.result = c.decorateLocked(Evaluate(c.currentStatusLocked(c.now()), c.settings, c.desired), c.settings)
	return nil
}

// Recover clears a failed-closed navigation latch only after the operator has
// disabled automatic control and the server has observed a newer, checksum-
// valid STANDBY home display. It also clears the manual override that caused
// the failed transaction so recovery cannot immediately retry it.
func (c *Controller) Recover(status api.Status, settings Settings) error {
	if c == nil {
		return fmt.Errorf("fan-policy controller unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.nav.failed {
		return fmt.Errorf("fan-policy navigation is not failed closed")
	}
	if settings.Enabled {
		return fmt.Errorf("disable automatic fan-policy switching before clearing the failure")
	}
	if !status.RecentContact {
		return fmt.Errorf("cannot clear fan-policy failure without fresh protocol status")
	}
	if status.TX == nil || *status.TX {
		return fmt.Errorf("cannot clear fan-policy failure until protocol status verifies RX")
	}
	if normalizeOperatingState(status.OperatingState) != "standby" {
		return fmt.Errorf("cannot clear fan-policy failure until protocol status verifies STANDBY")
	}
	if c.lastDisplayGen <= c.nav.failureAfterGen || c.lastDisplayKey != "home" {
		return fmt.Errorf("cannot clear fan-policy failure until a newer checksum-valid home display is observed")
	}
	if c.displayTX == nil || *c.displayTX || c.displayOperate == nil || *c.displayOperate {
		return fmt.Errorf("cannot clear fan-policy failure until the LCD verifies STANDBY/RX at home")
	}

	previousOverride := c.manualOverride
	previousDuration := c.manualOverrideDuration
	previousUntil := c.manualOverrideUntil
	previousVerifyRequested := c.verifyRequested
	previousVerifyReason := c.verifyReason
	previousDesired := c.desired
	previousNav := c.nav
	previousMayBeInMenu := c.mayBeInMenu
	previousLastVerifiedScreen := c.lastVerifiedScreen
	previousBaseSettings := c.baseSettings
	previousSettings := c.settings
	c.manualOverride = PolicyUnknown
	c.manualOverrideDuration = 0
	c.manualOverrideUntil = time.Time{}
	c.verifyRequested = false
	c.verifyReason = ""
	c.desired = PolicyUnknown
	c.nav = navState{}
	c.mayBeInMenu = false
	c.lastVerifiedScreen = "home:standby"
	c.baseSettings = settings
	c.settings = c.effectiveSettingsLocked(settings)
	c.persistLocked()
	if c.persistenceError != "" {
		persistErr := c.persistenceError
		c.manualOverride = previousOverride
		c.manualOverrideDuration = previousDuration
		c.manualOverrideUntil = previousUntil
		c.verifyRequested = previousVerifyRequested
		c.verifyReason = previousVerifyReason
		c.desired = previousDesired
		c.nav = previousNav
		c.mayBeInMenu = previousMayBeInMenu
		c.lastVerifiedScreen = previousLastVerifiedScreen
		c.baseSettings = previousBaseSettings
		c.settings = previousSettings
		c.result = c.decorateLocked(Evaluate(status, c.settings, c.desired), c.settings)
		return fmt.Errorf("persist recovered fan-policy state: %s", persistErr)
	}
	c.nav = previousNav
	c.releaseLeaseLocked()
	c.nav = navState{}
	c.result = c.decorateLocked(Evaluate(status, c.settings, PolicyUnknown), c.settings)
	return nil
}

func (c *Controller) Observe(status api.Status, settings Settings) Result {
	return c.observe(status, settings, true, 0)
}

func (c *Controller) ObserveFromSerialSession(status api.Status, settings Settings, serialSessionGeneration uint64) Result {
	return c.observe(status, settings, true, serialSessionGeneration)
}

func (c *Controller) ObserveSerialSession(serialSessionGeneration uint64) {
	if c == nil || serialSessionGeneration == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if serialSessionGeneration <= c.serialSessionGeneration {
		return
	}
	c.serialSessionGeneration = serialSessionGeneration
	c.statusSerialSessionGeneration = 0
	c.passive = passiveSaveState{}
	if c.nav.active {
		c.failLocked("serial session changed during fan-policy navigation")
	}
	if c.currentConfidence == "verified-live" {
		c.clearVerifiedPolicyLocked()
		c.persistLocked()
	}
}

// UpdateSettings reevaluates the controller with a cached status snapshot
// without treating that snapshot as a new protocol observation.
func (c *Controller) UpdateSettings(status api.Status, settings Settings) Result {
	return c.observe(status, settings, false, 0)
}

func (c *Controller) observe(status api.Status, settings Settings, protocolObservation bool, serialSessionGeneration uint64) Result {
	if c == nil {
		return Evaluate(status, settings, PolicyUnknown)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if serialSessionGeneration != 0 && serialSessionGeneration != c.serialSessionGeneration {
		return c.result
	}

	now := c.now()
	c.expireOverrideLocked(now)
	c.baseSettings = settings
	settings = c.effectiveSettingsLocked(settings)
	previousDesired := c.desired
	next := Evaluate(status, settings, previousDesired)
	c.status = status
	c.statusAt = statusContactTime(status, now)
	c.settings = settings
	if protocolObservation {
		c.statusGeneration++
		if serialSessionGeneration != 0 {
			c.statusSerialSessionGeneration = serialSessionGeneration
		}
		if c.currentModel != "" && strings.TrimSpace(status.ModelName) != "" && !strings.EqualFold(c.currentModel, strings.TrimSpace(status.ModelName)) {
			c.clearVerifiedPolicyLocked()
			c.persistLocked()
		}
	}
	if c.passive.thirdSeries && ((!protocolObservation && !c.thirdSeriesPassiveStatusBindingAllowedLocked()) ||
		(protocolObservation && (serialSessionGeneration == 0 || !c.thirdSeriesPassiveStatusBindingAllowedLocked()))) {
		c.passive = passiveSaveState{}
	}

	switch {
	case !next.ControlActive:
		c.releaseLeaseLocked()
		c.desired = PolicyUnknown
		next.DesiredPolicy = PolicyUnknown
		if !c.nav.failed {
			c.nav = navState{}
		}
	case next.State == StateUnavailable:
		c.desired = PolicyUnknown
		next.DesiredPolicy = PolicyUnknown
		if c.nav.active {
			c.failLocked("status became unavailable during display navigation")
		}
	default:
		if next.DesiredPolicy != PolicyUnknown {
			c.desired = next.DesiredPolicy
		}
		if c.nav.step == "complete" && previousDesired != PolicyUnknown && c.desired != previousDesired {
			c.nav = navState{}
		}
	}
	if c.nav.active {
		blocks := c.navigationBlocksLocked(status, settings)
		switch {
		case len(fatalNavigationBlocks(blocks)) != 0:
			c.failLocked("safety precondition changed during display navigation")
		case status.TX != nil && *status.TX:
			c.pauseLocked("transmission active; all fan-policy menu writes are paused until fresh RX")
		case c.nav.step == "standby:status":
			c.observeOperatingTransitionLocked(status, "standby", "standby:display", now)
		case c.nav.step == "restore-operate:status":
			c.observeOperatingTransitionLocked(status, "operate", "restore-operate:display", now)
		case c.nav.paused && c.nav.rxStatusGen == 0 && status.TX != nil && !*status.TX && c.statusGeneration > c.nav.pauseStatusGen:
			c.nav.rxStatusGen = c.statusGeneration
			c.nav.resumeAfterGen = c.lastDisplayGen
			c.nav.resumeDeadline = now.Add(navigationTimeout)
		}
	}
	next = c.applyCooldownLocked(next, now)
	c.result = c.decorateLocked(next, settings)
	return c.result
}

func (c *Controller) ObserveDisplay(observation DisplayObservation) Result {
	if c == nil {
		return Result{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if observation.SerialSessionGeneration != 0 && observation.SerialSessionGeneration != c.serialSessionGeneration {
		return c.result
	}

	now := c.now()
	previousDisplayGeneration := c.lastDisplayGen
	key := semanticDisplayKey(observation.State)
	c.lastDisplayGen = observation.Generation
	c.lastDisplayKey = key
	c.lastDisplayState = observation.State
	if observation.TX == nil {
		c.displayTX = nil
	} else {
		tx := *observation.TX
		c.displayTX = &tx
	}
	if observation.Operate == nil {
		c.displayOperate = nil
	} else {
		operate := *observation.Operate
		c.displayOperate = &operate
	}
	if !c.nav.active && (observation.Generation == 0 ||
		(previousDisplayGeneration != 0 && observation.Generation <= previousDisplayGeneration)) {
		c.passive = passiveSaveState{}
	} else if !c.nav.active {
		c.observePassiveSaveLocked(observation)
	}
	status := c.currentStatusLocked(now)
	base := Evaluate(status, c.settings, c.desired)
	blocks := c.navigationBlocksLocked(status, c.settings)
	if c.nav.active && c.nav.serialSessionGeneration != 0 && observation.SerialSessionGeneration != c.nav.serialSessionGeneration {
		c.failLocked("serial session changed during fan-policy display navigation")
		base = Evaluate(status, c.settings, c.desired)
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if !base.ControlActive || base.State == StateUnavailable || len(fatalNavigationBlocks(blocks)) != 0 {
		if c.nav.active {
			c.failLocked("safety precondition changed during display navigation")
			base = Evaluate(status, c.settings, c.desired)
		}
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if hasPauseBlock(blocks) {
		if c.nav.active {
			c.pauseLocked(pauseReason(blocks))
		}
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if c.nav.active && (c.nav.step == "standby:status" || c.nav.step == "restore-operate:status") {
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	resumed := false
	if c.nav.active && c.nav.paused {
		if c.nav.rxStatusGen <= c.nav.pauseStatusGen || observation.Generation <= c.nav.resumeAfterGen {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.nav.paused = false
		c.nav.pauseReason = ""
		c.nav.resumeDeadline = time.Time{}
		c.nav.deadline = now.Add(navigationTimeout)
		resumed = true
	}
	if c.nav.active && c.nav.step == "standby:display" {
		if observation.Generation <= c.nav.afterGeneration {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		if !matchesStandbyHome(observation.State) || observation.Operate == nil || *observation.Operate {
			c.unexpectedLocked(observation.State)
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.lastVerifiedScreen = "home:standby"
		if !c.sendLocked("set", "setup:identify", observation.Generation, "home") {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if c.nav.active && c.nav.step == "restore-operate:display" {
		if observation.Generation <= c.nav.afterGeneration {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		if !matchesOperateHome(observation.State) || observation.Operate == nil || !*observation.Operate {
			c.unexpectedLocked(observation.State)
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.lastVerifiedScreen = "home:operate"
		if c.nav.verifyOnly {
			c.verifyRequested = false
			c.verifyReason = ""
			c.nav.verifyOnly = false
			c.settings = c.effectiveSettingsLocked(c.baseSettings)
		}
		c.nav.active = false
		c.nav.step = "complete"
		c.nav.restoreOperate = false
		c.nav.controlUncertain = false
		c.nav.deadline = time.Time{}
		c.releaseLeaseLocked()
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if c.nav.active && c.nav.step == "recover:home" {
		if observation.Generation <= c.nav.afterGeneration {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		if !matchesStandbyHome(observation.State) || observation.Operate == nil || *observation.Operate {
			c.failLocked("verified DISPLAY recovery did not return to a checksum-valid STANDBY/RX home display")
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.lastVerifiedScreen = "home:standby"
		c.mayBeInMenu = false
		c.nav.active = false
		c.nav.failed = true
		c.nav.recoveryVerified = true
		c.nav.deadline = time.Time{}
		c.releaseLeaseLocked()
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if resumed {
		if !c.advanceLocked(observation, key) {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	base = c.applyCooldownLocked(base, now)
	if containsBlock(base.BlockedBy, "cooldown") {
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if c.desired == PolicyUnknown && !c.verifyRequested {
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if !c.nav.active && !c.verifyRequested &&
		c.current == c.desired && c.current != PolicyUnknown &&
		c.currentConfidence == "verified-live" {
		base.Pending = false
		base.State = StateNormal
		base.Reason = "fan policy already matches the desired policy"
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if c.nav.failed {
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}

	if !c.nav.active {
		if key != "home" {
			base.State = StateBlocked
			base.Pending = false
			base.BlockedBy = []string{"verified-home-display"}
			base.Reason = "fan-policy change is waiting for a verified Expert home display"
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		if _, sessionBound := availableSerialSessionTransport(c.transport); sessionBound &&
			(c.serialSessionGeneration == 0 || c.statusSerialSessionGeneration != c.serialSessionGeneration || observation.SerialSessionGeneration != c.serialSessionGeneration) {
			base.State = StateBlocked
			base.Pending = false
			base.BlockedBy = appendUnique(base.BlockedBy, "serial-session")
			base.Reason = "fan-policy change is waiting for status and display evidence from the same live serial session"
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		if !c.acquireLeaseLocked() {
			base.State = StateBlocked
			base.Pending = false
			base.BlockedBy = appendUnique(base.BlockedBy, "actuation-lease")
			base.Reason = "fan-policy change is waiting for exclusive automatic actuation ownership"
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.lastVerifiedScreen = key
		c.nav.active = true
		c.nav.leaseHeld = true
		c.nav.model = strings.TrimSpace(status.ModelName)
		c.nav.serialSessionGeneration = c.serialSessionGeneration
		c.nav.verifyOnly = c.verifyRequested
		c.nav.target = c.desired
		operatingState := normalizeOperatingState(status.OperatingState)
		switch operatingState {
		case "operate":
			if !matchesOperateHome(observation.State) || observation.Operate == nil || !*observation.Operate {
				c.failLocked("OPERATE status did not match a checksum-valid OPERATE home display")
			} else {
				c.nav.restoreOperate = true
				c.nav.controlUncertain = true
				c.nav.afterStatusGen = c.statusGeneration
				c.nav.step = "standby:status"
				c.nav.deadline = now.Add(navigationTimeout)
				c.sendControlLocked("operate")
			}
		case "standby":
			if !matchesStandbyHome(observation.State) || observation.Operate == nil || *observation.Operate {
				c.failLocked("STANDBY status did not match a checksum-valid STANDBY home display")
			} else {
				c.lastVerifiedScreen = "home:standby"
				c.sendLocked("set", "setup:identify", observation.Generation, "home")
			}
		default:
			c.failLocked("operating state was not explicit before fan-policy navigation")
		}
		if c.nav.failed {
			c.result = c.decorateLocked(base, c.settings)
			return c.result
		}
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}

	if observation.Generation <= c.nav.afterGeneration || key == c.nav.previousKey {
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	if !c.advanceLocked(observation, key) {
		c.result = c.decorateLocked(base, c.settings)
		return c.result
	}
	c.result = c.decorateLocked(base, c.settings)
	return c.result
}

func statusContactTime(status api.Status, now time.Time) time.Time {
	if !status.RecentContact {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, status.LastContactAt); err == nil {
		return parsed
	}
	// Direct callers and older fixtures may not include LastContactAt. Observe
	// itself is then the freshest evidence available.
	return now
}

func (c *Controller) Tick(now time.Time) Result {
	if c == nil {
		return Result{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireOverrideLocked(now)
	c.settings = c.effectiveSettingsLocked(c.baseSettings)
	if c.nav.active {
		status := c.currentStatusLocked(now)
		if fatal := fatalNavigationBlocks(c.navigationBlocksLocked(status, c.settings)); len(fatal) != 0 {
			c.failLocked("safety precondition became unavailable during display navigation")
		} else if c.nav.paused && !c.nav.resumeDeadline.IsZero() && now.After(c.nav.resumeDeadline) {
			message := "timed out waiting for the exact expected LCD waypoint after fresh RX"
			if !c.startVerifiedSetupExitLocked(message) {
				c.failLocked(message)
			}
		} else if !c.nav.paused && !c.nav.deadline.IsZero() && now.After(c.nav.deadline) {
			message := "timed out waiting for the expected newer display state"
			if !c.startVerifiedSetupExitLocked(message) {
				c.failLocked(message)
			}
		}
	}
	status := c.currentStatusLocked(now)
	c.result = c.decorateLocked(c.applyCooldownLocked(Evaluate(status, c.settings, c.desired), now), c.settings)
	return c.result
}

func (c *Controller) Current() Result {
	if c == nil {
		return Result{State: StateDisabled, DesiredPolicy: PolicyUnknown, CurrentPolicy: PolicyUnknown}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.result
}

func (c *Controller) View(status api.Status, settings Settings) Result {
	if c == nil {
		return Evaluate(status, settings, PolicyUnknown)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	settings = c.effectiveSettingsLocked(settings)
	return c.decorateLocked(c.applyCooldownLocked(Evaluate(status, settings, c.desired), c.now()), settings)
}

func (c *Controller) advanceLocked(observation DisplayObservation, key string) bool {
	if strings.HasPrefix(c.nav.step, "setup:") && c.nav.step != "setup:identify" && c.nav.profile != "" {
		return c.advanceProfileSetupLocked(observation, key)
	}
	switch c.nav.step {
	case "setup:identify":
		profile, initialKey, ok := identifyFanDisplayProfile(observation.State, c.status.ModelName, c.settings.FirmwareVersion)
		if !ok || key != initialKey {
			return c.unexpectedLocked(observation.State)
		}
		c.nav.profile = profile.id
		c.nav.setupIndex = 0
		return c.advanceProfileSetupLocked(observation, key)
	case "third-fan:entry":
		selected, active, ok := ThirdSeriesFanScreen(observation.State)
		if !ok {
			return c.unexpectedLocked(observation.State)
		}
		c.nav.observedPolicy = active
		if c.nav.verifyOnly {
			c.nav.target = active
		}
		return c.advanceThirdSeriesTargetLocked(observation, key, selected, active)
	case "third-fan:target":
		selected, active, ok := ThirdSeriesFanScreen(observation.State)
		if !ok || selected != c.nav.expectedThirdSelection || active != c.nav.observedPolicy {
			return c.unexpectedLocked(observation.State)
		}
		return c.advanceThirdSeriesTargetLocked(observation, key, selected, active)
	case "third-fan:toggled":
		selected, active, ok := ThirdSeriesFanScreen(observation.State)
		if !ok || selected != thirdSeriesSelectionForPolicy(c.nav.target) || active != c.nav.target {
			return c.unexpectedLocked(observation.State)
		}
		c.nav.observedPolicy = active
		return c.sendThirdSeriesRightLocked("third-fan:save", observation.Generation, key, selected)
	case "third-fan:save":
		selected, active, ok := ThirdSeriesFanScreen(observation.State)
		if !ok || selected != c.nav.expectedThirdSelection || active != c.nav.target {
			return c.unexpectedLocked(observation.State)
		}
		if selected == "save" {
			return c.sendLocked("set", "submenu:STORING", observation.Generation, key)
		}
		return c.sendThirdSeriesRightLocked("third-fan:save", observation.Generation, key, selected)
	case "submenu:TEMPERATURE SCALE":
		policy, ok := submenuPolicy(observation.State, "TEMPERATURE SCALE")
		if !ok {
			return c.unexpectedLocked(observation.State)
		}
		c.lastVerifiedScreen = key
		c.nav.observedPolicy = policy
		return c.sendLocked("right", "submenu:FAN MANAGEMENT", observation.Generation, key)
	case "submenu:FAN MANAGEMENT":
		policy, ok := submenuPolicy(observation.State, "FAN MANAGEMENT")
		if !ok {
			return c.unexpectedLocked(observation.State)
		}
		c.lastVerifiedScreen = key
		c.nav.observedPolicy = policy
		if c.nav.verifyOnly {
			c.nav.target = policy
		}
		if policy == c.nav.target {
			return c.sendLocked("right", "submenu:SAVE", observation.Generation, key)
		}
		return c.sendLocked("set", "submenu:TOGGLED", observation.Generation, key)
	case "submenu:TOGGLED":
		policy, ok := submenuPolicy(observation.State, "FAN MANAGEMENT")
		if !ok || policy != c.nav.target {
			return c.unexpectedLocked(observation.State)
		}
		c.lastVerifiedScreen = key
		c.nav.observedPolicy = policy
		return c.sendLocked("right", "submenu:SAVE", observation.Generation, key)
	case "submenu:SAVE":
		if !matchesSubmenuSelection(observation.State, "SAVE") {
			return c.unexpectedLocked(observation.State)
		}
		c.lastVerifiedScreen = key
		return c.sendLocked("set", "submenu:STORING", observation.Generation, key)
	case "submenu:STORING":
		if matchesStandbyHome(observation.State) && observation.Operate != nil && !*observation.Operate {
			// STORING DATA! can be shorter than one display-poll interval.
			// A newer verified Standby home after our SET-on-SAVE write is the
			// same bounded success path; passive observation remains stricter.
			c.nav.step = "home"
			return c.advanceLocked(observation, key)
		}
		if !matchesStoring(observation.State) && !(c.nav.profile == ThirdSeriesDisplayProfile && matchesThirdSeriesStoring(observation.State)) {
			return c.unexpectedLocked(observation.State)
		}
		c.lastVerifiedScreen = key
		c.nav.step = "home"
		c.nav.afterGeneration = observation.Generation
		c.nav.previousKey = key
		c.nav.deadline = c.now().Add(navigationTimeout)
		return true
	case "home":
		if !matchesStandbyHome(observation.State) || observation.Operate == nil || *observation.Operate {
			return c.unexpectedLocked(observation.State)
		}
		now := c.now()
		c.lastVerifiedScreen = "home:standby"
		c.mayBeInMenu = false
		source := "automatic-temperature"
		if c.nav.verifyOnly {
			source = c.verifyReason + "-verification"
		} else if c.manualOverride != PolicyUnknown {
			source = "manual-override"
		}
		c.recordVerifiedPolicyLocked(c.nav.target, now, source)
		if c.nav.restoreOperate {
			if leased, ok := c.transport.(leaseButtonTransport); ok && leased.SafetyHold() {
				c.failLocked("overtemperature safety hold suppressed OPERATE restoration; amplifier remains in STANDBY")
				return false
			}
			c.nav.controlUncertain = true
			c.nav.afterStatusGen = c.statusGeneration
			c.nav.step = "restore-operate:status"
			c.nav.deadline = now.Add(navigationTimeout)
			return c.sendControlLocked("operate")
		}
		c.nav.active = false
		c.nav.step = "complete"
		c.nav.deadline = time.Time{}
		if c.nav.verifyOnly {
			c.verifyRequested = false
			c.verifyReason = ""
			c.nav.verifyOnly = false
			c.settings = c.effectiveSettingsLocked(c.baseSettings)
		}
		c.releaseLeaseLocked()
		return true
	default:
		c.failLocked("unknown display-navigation state")
		return false
	}
}

func (c *Controller) advanceProfileSetupLocked(observation DisplayObservation, key string) bool {
	profile, ok := fanDisplayProfileByID(c.nav.profile)
	if !ok || c.nav.setupIndex < 0 || c.nav.setupIndex >= len(profile.setupKeys) {
		return c.unexpectedLocked(observation.State)
	}
	expected := profile.setupKeys[c.nav.setupIndex]
	if key != expected || !matchesProfileSetupSelection(observation.State, profile.id, expected) {
		return c.unexpectedLocked(observation.State)
	}
	c.lastVerifiedScreen = key
	if c.nav.setupIndex == len(profile.setupKeys)-1 {
		next := "submenu:TEMPERATURE SCALE"
		if profile.id == ThirdSeriesDisplayProfile {
			next = "third-fan:entry"
		}
		return c.sendLocked("set", next, observation.Generation, key)
	}
	c.nav.setupIndex++
	return c.sendLocked("right", profile.setupKeys[c.nav.setupIndex], observation.Generation, key)
}

func (c *Controller) advanceThirdSeriesTargetLocked(observation DisplayObservation, key, selected, active string) bool {
	targetSelection := thirdSeriesSelectionForPolicy(c.nav.target)
	if targetSelection == "" {
		return c.unexpectedLocked(observation.State)
	}
	if active == c.nav.target {
		return c.sendThirdSeriesRightLocked("third-fan:save", observation.Generation, key, selected)
	}
	if selected == targetSelection {
		return c.sendLocked("set", "third-fan:toggled", observation.Generation, key)
	}
	return c.sendThirdSeriesRightLocked("third-fan:target", observation.Generation, key, selected)
}

func (c *Controller) sendThirdSeriesRightLocked(step string, generation uint64, key, selected string) bool {
	c.nav.expectedThirdSelection = nextThirdSeriesSelection(selected)
	if c.nav.expectedThirdSelection == "" {
		return false
	}
	return c.sendLocked("right", step, generation, key)
}

func (c *Controller) sendLocked(action, nextStep string, generation uint64, previousKey string) bool {
	blocks := c.navigationBlocksLocked(c.currentStatusLocked(c.now()), c.settings)
	if fatal := fatalNavigationBlocks(blocks); len(fatal) != 0 {
		c.failLocked("safety precondition changed before display-navigation write")
		return false
	}
	if hasPauseBlock(blocks) {
		c.pauseLocked(pauseReason(blocks))
		return false
	}
	c.nav.step = nextStep
	c.nav.afterGeneration = generation
	c.nav.previousKey = previousKey
	c.nav.deadline = c.now().Add(navigationTimeout)
	if (previousKey == "home" || previousKey == "home:standby") && action == "set" {
		// A transport error cannot prove that SET did not reach the amplifier.
		c.mayBeInMenu = true
	}
	if c.transport == nil {
		c.failLocked("button transport unavailable")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	result, err := c.sendButtonForNavigationLocked(ctx, action, "standby")
	if err != nil || !result.Sent {
		if err != nil {
			c.failLocked(fmt.Sprintf("send %s: %v", action, err))
		} else {
			c.failLocked(fmt.Sprintf("send %s was not confirmed written", action))
		}
		return false
	}
	c.nav.lastAction = action
	c.nav.actions = append(c.nav.actions, action)
	return true
}

func (c *Controller) sendControlLocked(action string) bool {
	status := c.currentStatusLocked(c.now())
	blocks := c.navigationBlocksLocked(status, c.settings)
	if fatal := fatalNavigationBlocks(blocks); len(fatal) != 0 {
		c.failLocked("safety precondition changed before operating-state command")
		return false
	}
	if hasPauseBlock(blocks) {
		c.pauseLocked(pauseReason(blocks))
		return false
	}
	expectedCurrent := "operate"
	if c.nav.step == "restore-operate:status" {
		expectedCurrent = "standby"
	}
	if normalizeOperatingState(status.OperatingState) != expectedCurrent ||
		c.displayOperate == nil ||
		(*c.displayOperate != (expectedCurrent == "operate")) {
		c.failLocked("protocol and LCD operating state did not agree before operating-state command")
		return false
	}
	if c.transport == nil {
		c.failLocked("button transport unavailable")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	result, err := c.sendButtonForNavigationLocked(ctx, action, expectedCurrent)
	if err != nil || !result.Sent {
		if err != nil {
			c.failLocked(fmt.Sprintf("send %s: %v", action, err))
		} else {
			c.failLocked(fmt.Sprintf("send %s was not confirmed written", action))
		}
		return false
	}
	c.nav.lastAction = action
	c.nav.actions = append(c.nav.actions, action)
	return true
}

func (c *Controller) sendButtonForNavigationLocked(ctx context.Context, action, expectedOperatingState string) (api.ActionResult, error) {
	if sessionTransport, ok := availableSerialSessionTransport(c.transport); ok {
		if c.nav.serialSessionGeneration == 0 || strings.TrimSpace(c.nav.model) == "" {
			return api.ActionResult{Name: action}, errors.New("fan-policy serial session and model are not bound")
		}
		return sessionTransport.SendButtonForSerialSession(ctx, api.ButtonAction{Name: action}, transport.SerialSessionWriteAuthorization{
			SessionGeneration:      c.nav.serialSessionGeneration,
			Model:                  c.nav.model,
			ExpectedOperatingState: expectedOperatingState,
		})
	}
	return c.transport.SendButton(ctx, api.ButtonAction{Name: action})
}

func availableSerialSessionTransport(buttonTransport ButtonTransport) (transport.SerialSessionButtonTransport, bool) {
	sessionTransport, ok := buttonTransport.(transport.SerialSessionButtonTransport)
	if !ok {
		return nil, false
	}
	if capability, wrapped := buttonTransport.(transport.SerialSessionWriteCapability); wrapped && !capability.SerialSessionWritesAvailable() {
		return nil, false
	}
	return sessionTransport, true
}

func (c *Controller) observeOperatingTransitionLocked(status api.Status, expectedState, displayStep string, now time.Time) {
	if c.nav.paused {
		if status.TX == nil || *status.TX || c.statusGeneration <= c.nav.pauseStatusGen {
			return
		}
		c.nav.paused = false
		c.nav.pauseReason = ""
		c.nav.deadline = now.Add(navigationTimeout)
	}
	if c.statusGeneration <= c.nav.afterStatusGen ||
		status.TX == nil ||
		*status.TX ||
		normalizeOperatingState(status.OperatingState) != expectedState {
		return
	}
	c.nav.step = displayStep
	c.nav.afterGeneration = c.lastDisplayGen
	c.nav.deadline = now.Add(navigationTimeout)
	c.nav.controlUncertain = false
	if expectedState == "standby" {
		c.nav.changedOperate = true
	}
}

func normalizeOperatingState(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (c *Controller) unexpectedLocked(state display.State) bool {
	message := fmt.Sprintf("unexpected display while waiting for %s: %q", c.nav.step, firstNonBlankRow(state))
	if strings.HasPrefix(semanticDisplayKey(state), "setup:") && c.startVerifiedSetupExitLocked(message) {
		return false
	}
	c.failLocked(message)
	return false
}

// startVerifiedSetupExitLocked uses DISPLAY only for exact promoted setup
// topologies where field testing proved it returns the amplifier home without
// saving. It is intentionally limited to unattended startup verification.
// Every other model, screen family, and transaction still requires
// physical-panel recovery after a mismatch or timeout.
func (c *Controller) startVerifiedSetupExitLocked(cause string) bool {
	currentSetupEvidence := c.lastDisplayGen > c.nav.afterGeneration
	pendingSetupMove := c.nav.lastAction == "right" && strings.HasPrefix(c.nav.step, "setup:") &&
		strings.HasPrefix(c.nav.previousKey, "setup:")
	profile, knownProfile := fanDisplayProfileByID(c.nav.profile)
	if !c.nav.active || c.nav.recoveryAttempted || !c.nav.verifyOnly || c.verifyReason != "startup" ||
		!knownProfile || !profile.startupSetupDisplayExitVerified ||
		!strings.HasPrefix(c.nav.step, "setup:") ||
		(!currentSetupEvidence && !pendingSetupMove) ||
		!matchesProfileSetupSelection(c.lastDisplayState, profile.id, c.lastDisplayKey) {
		return false
	}
	c.nav.recoveryAttempted = true
	c.nav.lastError = cause
	c.nav.failureAfterGen = c.lastDisplayGen
	if !c.sendLocked("display", "recover:home", c.lastDisplayGen, c.lastDisplayKey) && c.nav.paused {
		return false
	}
	return true
}

func (c *Controller) failLocked(message string) {
	c.nav.active = false
	c.nav.failed = true
	c.nav.paused = false
	c.nav.pauseReason = ""
	c.nav.resumeDeadline = time.Time{}
	c.nav.lastError = message
	c.nav.failureAfterGen = c.lastDisplayGen
	c.nav.deadline = time.Time{}
	c.releaseLeaseLocked()
}

func (c *Controller) acquireLeaseLocked() bool {
	if c.nav.leaseHeld {
		return true
	}
	if transport, ok := c.transport.(leaseButtonTransport); ok && !transport.Acquire() {
		return false
	}
	c.nav.leaseHeld = true
	return true
}

func (c *Controller) releaseLeaseLocked() {
	if !c.nav.leaseHeld {
		return
	}
	if transport, ok := c.transport.(leaseButtonTransport); ok {
		transport.Release()
	}
	c.nav.leaseHeld = false
}

func (c *Controller) pauseLocked(reason string) {
	if !c.nav.active {
		return
	}
	c.nav.paused = true
	c.nav.pauseReason = reason
	c.nav.pauseStatusGen = c.statusGeneration
	c.nav.rxStatusGen = 0
	c.nav.resumeAfterGen = c.lastDisplayGen
	c.nav.resumeDeadline = time.Time{}
	c.nav.deadline = time.Time{}
}

func (c *Controller) currentStatusLocked(now time.Time) api.Status {
	status := c.status
	if c.statusAt.IsZero() || now.Sub(c.statusAt) > statusContactWindow {
		status.RecentContact = false
	}
	return status
}

func (c *Controller) navigationBlocksLocked(status api.Status, settings Settings) []string {
	blocks := actionBlocks(status, settings)
	if c.nav.active && !strings.EqualFold(strings.TrimSpace(status.ModelName), c.nav.model) {
		blocks = appendUnique(blocks, "model-changed")
	}
	if c.nav.active && c.nav.serialSessionGeneration != 0 &&
		(c.serialSessionGeneration != c.nav.serialSessionGeneration || c.statusSerialSessionGeneration != c.nav.serialSessionGeneration) {
		blocks = appendUnique(blocks, "serial-session-changed")
	}
	if c.nav.active && requiresStandby(c.nav.step) && normalizeOperatingState(status.OperatingState) != "standby" {
		blocks = appendUnique(blocks, "standby-required")
	}
	switch {
	case c.displayTX == nil:
		blocks = appendUnique(blocks, "known-lcd-rx")
	case *c.displayTX:
		blocks = appendUnique(blocks, "rx")
	}
	return blocks
}

func requiresStandby(step string) bool {
	return step != "" &&
		step != "standby:status" &&
		step != "restore-operate:status" &&
		step != "restore-operate:display" &&
		step != "complete"
}

func fatalNavigationBlocks(blocks []string) []string {
	fatal := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block != "rx" && block != "known-lcd-rx" {
			fatal = append(fatal, block)
		}
	}
	return fatal
}

func hasPauseBlock(blocks []string) bool {
	return containsBlock(blocks, "rx") || containsBlock(blocks, "known-lcd-rx")
}

func pauseReason(blocks []string) string {
	if containsBlock(blocks, "rx") {
		return "transmission active; all fan-policy menu writes are paused until fresh RX"
	}
	return "waiting for checksum-valid LCD RX evidence before the next fan-policy menu write"
}

func (c *Controller) effectiveSettingsLocked(settings Settings) Settings {
	settings.OverridePolicy = c.manualOverride
	settings.VerifyRequested = c.verifyRequested
	return settings
}

func (c *Controller) expireOverrideLocked(now time.Time) {
	if c.manualOverride == PolicyUnknown || c.manualOverrideUntil.IsZero() || now.Before(c.manualOverrideUntil) {
		return
	}
	c.manualOverride = PolicyUnknown
	c.manualOverrideDuration = 0
	c.manualOverrideUntil = time.Time{}
	c.desired = PolicyUnknown
	if c.nav.step == "complete" {
		c.nav = navState{}
	}
	c.persistLocked()
}

func (c *Controller) recordVerifiedPolicyLocked(policy string, verifiedAt time.Time, source string) {
	policy = normalizePolicy(policy)
	if policy == PolicyUnknown {
		return
	}
	c.current = policy
	c.currentVerifiedAt = verifiedAt
	c.currentConfidence = "verified-live"
	c.currentSource = strings.TrimSpace(source)
	c.currentModel = strings.TrimSpace(c.status.ModelName)
	if c.nav.model != "" {
		c.currentModel = c.nav.model
	}
	if c.currentSource == "manual-override" && c.manualOverrideDuration > 0 && c.manualOverrideUntil.IsZero() {
		c.manualOverrideUntil = verifiedAt.Add(c.manualOverrideDuration)
		c.manualOverrideDuration = 0
	}
	if c.current == PolicyHigh {
		c.lastVerifiedHighAt = verifiedAt
	} else {
		c.lastVerifiedHighAt = time.Time{}
	}
	c.persistLocked()
}

func (c *Controller) clearVerifiedPolicyLocked() {
	c.current = PolicyUnknown
	c.currentVerifiedAt = time.Time{}
	c.currentConfidence = "unknown"
	c.currentSource = ""
	c.currentModel = ""
	c.lastVerifiedHighAt = time.Time{}
}

func (c *Controller) observePassiveSaveLocked(observation DisplayObservation) {
	if c.thirdSeriesModelLocked() {
		c.observeThirdSeriesPassiveSaveLocked(observation)
		return
	}
	if c.passive.thirdSeries {
		c.passive = passiveSaveState{}
	}
	if policy, ok := submenuPolicy(observation.State, "FAN MANAGEMENT"); ok {
		c.passive.candidatePolicy = policy
		c.passive.sawSave = false
		c.passive.sawStoring = false
		c.passive.thirdSeries = false
		return
	}
	if matchesSubmenuSelection(observation.State, "SAVE") && c.passive.candidatePolicy != "" {
		c.passive.sawSave = true
		return
	}
	if matchesStoring(observation.State) && c.passive.sawSave {
		c.passive.sawStoring = true
		return
	}
	if !matchesHome(observation.State) {
		return
	}
	if c.passive.sawStoring {
		c.recordVerifiedPolicyLocked(c.passive.candidatePolicy, c.now(), "observed-front-panel-save")
	}
	c.passive = passiveSaveState{}
}

func (c *Controller) observeThirdSeriesPassiveSaveLocked(observation DisplayObservation) {
	if c.passive.thirdSeries {
		c.observeThirdSeriesPassiveContinuationLocked(observation)
		return
	}
	c.passive = passiveSaveState{}
	selected, policy, ok := ThirdSeriesFanScreen(observation.State)
	if !ok || selected == "save" || !c.thirdSeriesPassiveEvidenceAllowedLocked(observation) {
		return
	}
	c.passive = passiveSaveState{
		candidatePolicy: policy,
		thirdSeries:     true,
		phase:           "fan",
		model:           strings.TrimSpace(c.status.ModelName),
		firmware:        strings.TrimSpace(c.settings.FirmwareVersion),
		serialSession:   observation.SerialSessionGeneration,
		lastGeneration:  observation.Generation,
	}
}

func (c *Controller) observeThirdSeriesPassiveContinuationLocked(observation DisplayObservation) {
	if !c.thirdSeriesPassiveEvidenceAllowedLocked(observation) || observation.Generation <= c.passive.lastGeneration {
		c.passive = passiveSaveState{}
		return
	}
	c.passive.lastGeneration = observation.Generation
	switch c.passive.phase {
	case "fan":
		selected, policy, ok := ThirdSeriesFanScreen(observation.State)
		if !ok {
			c.passive = passiveSaveState{}
			return
		}
		if selected == "save" {
			if policy != c.passive.candidatePolicy {
				c.passive = passiveSaveState{}
				return
			}
			c.passive.phase = "save"
			return
		}
		c.passive.candidatePolicy = policy
	case "save":
		if selected, policy, ok := ThirdSeriesFanScreen(observation.State); ok && selected == "save" && policy == c.passive.candidatePolicy {
			return
		}
		if !matchesThirdSeriesStoring(observation.State) {
			c.passive = passiveSaveState{}
			return
		}
		c.passive.phase = "storing"
	case "storing":
		if matchesThirdSeriesStoring(observation.State) {
			return
		}
		if !matchesStandbyHome(observation.State) {
			c.passive = passiveSaveState{}
			return
		}
		c.recordVerifiedPolicyLocked(c.passive.candidatePolicy, c.now(), "observed-front-panel-save")
		c.passive = passiveSaveState{}
	default:
		c.passive = passiveSaveState{}
	}
}

func (c *Controller) thirdSeriesPassiveEvidenceAllowedLocked(observation DisplayObservation) bool {
	if !c.thirdSeriesPassiveStatusBindingAllowedLocked() || observation.Generation == 0 ||
		observation.SerialSessionGeneration == 0 || observation.SerialSessionGeneration != c.serialSessionGeneration ||
		observation.TX == nil || *observation.TX || observation.Operate == nil || *observation.Operate {
		return false
	}
	if !c.passive.thirdSeries {
		return true
	}
	return c.passive.serialSession == observation.SerialSessionGeneration &&
		c.passive.model == strings.TrimSpace(c.status.ModelName) &&
		c.passive.firmware == strings.TrimSpace(c.settings.FirmwareVersion)
}

func (c *Controller) thirdSeriesPassiveStatusBindingAllowedLocked() bool {
	profile, bound := verifiedFanDisplayProfileForModel(c.status.ModelName, c.settings.FirmwareVersion)
	status := c.currentStatusLocked(c.now())
	return bound && profile.id == ThirdSeriesDisplayProfile && c.serialSessionGeneration != 0 &&
		c.statusSerialSessionGeneration == c.serialSessionGeneration && status.RecentContact &&
		normalizeOperatingState(status.OperatingState) == "standby" && status.TX != nil && !*status.TX
}

func (c *Controller) thirdSeriesModelLocked() bool {
	profile, ok := fanDisplayProfileByID(ThirdSeriesDisplayProfile)
	return ok && strings.EqualFold(strings.TrimSpace(c.status.ModelName), profile.model)
}

func (c *Controller) persistentStateLocked() PersistentState {
	state := PersistentState{}
	if c.manualOverride != PolicyUnknown {
		state.ManualOverride = c.manualOverride
	}
	if state.ManualOverride != "" && c.manualOverrideDuration > 0 {
		state.ManualOverrideDurationMinutes = int(c.manualOverrideDuration / time.Minute)
	}
	if state.ManualOverride != "" && !c.manualOverrideUntil.IsZero() {
		state.ManualOverrideUntil = c.manualOverrideUntil.UTC().Format(time.RFC3339)
	}
	if c.current != PolicyUnknown && !c.currentVerifiedAt.IsZero() {
		state.LastVerifiedPolicy = c.current
		state.LastVerifiedAt = c.currentVerifiedAt.UTC().Format(time.RFC3339)
		state.LastVerifiedSource = c.currentSource
		state.LastVerifiedModel = c.currentModel
	}
	return state
}

func (c *Controller) persistLocked() {
	if c.persist == nil {
		return
	}
	if err := c.persist(c.persistentStateLocked()); err != nil {
		c.persistenceError = err.Error()
		return
	}
	c.persistenceError = ""
}

func (c *Controller) applyCooldownLocked(base Result, now time.Time) Result {
	if base.DesiredPolicy != PolicyNormal {
		return base
	}
	if c.manualOverride == PolicyNormal {
		return base
	}
	var until time.Time
	if !c.lastVerifiedHighAt.IsZero() {
		until = c.lastVerifiedHighAt.Add(normalRestoreCooldown)
	}
	if c.currentConfidence != "verified-live" && c.normalRestoreAfter.After(until) {
		until = c.normalRestoreAfter
	}
	if until.IsZero() {
		return base
	}
	if !now.Before(until) {
		return base
	}
	base.State = StateBlocked
	base.Pending = false
	base.BlockedBy = appendUnique(base.BlockedBy, "cooldown")
	base.Reason = "Normal fan restore is waiting for the conservative write cooldown; high cooling is never delayed"
	base.CooldownUntil = until.UTC().Format(time.RFC3339)
	return base
}

func appendUnique(values []string, value string) []string {
	if containsBlock(values, value) {
		return values
	}
	return append(values, value)
}

func containsBlock(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *Controller) decorateLocked(base Result, settings Settings) Result {
	base.CurrentPolicy = c.current
	base.CurrentPolicyConfidence = c.currentConfidence
	base.CurrentPolicySource = c.currentSource
	base.PersistenceError = c.persistenceError
	base.ManualOverride = ManualOverride{Active: c.manualOverride != PolicyUnknown}
	if base.ManualOverride.Active {
		base.ManualOverride.Policy = c.manualOverride
	}
	if base.ManualOverride.Active && c.manualOverrideDuration > 0 {
		base.ManualOverride.DurationMinutes = int(c.manualOverrideDuration / time.Minute)
	}
	if base.ManualOverride.Active && !c.manualOverrideUntil.IsZero() {
		base.ManualOverride.Until = c.manualOverrideUntil.UTC().Format(time.RFC3339)
	}
	base.Verification = Verification{
		Requested: c.verifyRequested,
		Reason:    c.verifyReason,
	}
	if !c.currentVerifiedAt.IsZero() {
		base.CurrentPolicyVerifiedAt = c.currentVerifiedAt.UTC().Format(time.RFC3339)
	}
	_, supportedProfile := verifiedFanDisplayProfileForModel(base.Observations.ModelName, settings.FirmwareVersion)
	base.ActionAvailable = c.transport != nil && supportedProfile
	if base.ControlActive {
		switch {
		case c.displayTX == nil:
			base.BlockedBy = appendUnique(base.BlockedBy, "known-lcd-rx")
		case *c.displayTX:
			base.BlockedBy = appendUnique(base.BlockedBy, "rx")
		}
		if !c.nav.active && hasPauseBlock(base.BlockedBy) {
			base.State = StateBlocked
			base.Pending = false
			base.Reason = pauseReason(base.BlockedBy)
		}
	}
	base.Navigation = Navigation{
		State:                 "idle",
		Profile:               c.nav.profile,
		LastAction:            c.nav.lastAction,
		LastError:             c.nav.lastError,
		LastVerifiedScreen:    c.lastVerifiedScreen,
		ActionsTaken:          append([]string(nil), c.nav.actions...),
		Paused:                c.nav.paused,
		PauseReason:           c.nav.pauseReason,
		MayBeInMenu:           c.mayBeInMenu,
		ChangedOperatingState: c.nav.changedOperate,
		RestoreOperatePending: c.nav.restoreOperate && (c.nav.active || c.nav.failed),
		RecoveryState:         "not-needed",
	}
	if base.Navigation.ActionsTaken == nil {
		base.Navigation.ActionsTaken = []string{}
	}
	if c.nav.recoveryVerified {
		base.Navigation.RecoveryState = "verified-home"
	} else if c.mayBeInMenu || (c.nav.failed && (c.nav.changedOperate || c.nav.controlUncertain || c.nav.restoreOperate)) {
		base.Navigation.RecoveryState = "operator-required"
		base.Navigation.RecoveryInstructions = "Stop transmitting, put the amplifier in STANDBY, and use the physical front panel to return to the home screen. Disable automatic fan-policy switching, wait for fresh STANDBY/RX home evidence, then POST /api/v1/fan-policy/recover or use Clear Failed Fan Transaction in Settings. Recovery clears the manual override and latch without restoring OPERATE."
	}
	switch {
	case c.nav.failed:
		base.State = StateFailed
		base.Pending = false
		if c.nav.recoveryVerified {
			base.Reason = "fan-policy verification failed closed after a model-reviewed DISPLAY exit returned the amplifier to verified STANDBY home; disable the policy and clear the failed transaction before re-enabling"
		} else {
			base.Reason = "fan-policy navigation failed closed; disable the policy and follow the reported recovery guidance before re-enabling"
		}
		base.Navigation.State = "failed"
	case c.nav.active && c.nav.paused:
		base.State = StatePaused
		base.Pending = true
		base.Reason = c.nav.pauseReason
		base.Navigation.State = c.nav.step
	case c.nav.active:
		base.State = StateNavigating
		base.Pending = true
		base.Reason = "display-verified fan-policy navigation is in progress"
		base.Navigation.State = c.nav.step
	case c.nav.step == "complete":
		base.Navigation.State = "complete"
		if base.State == StatePending {
			base.State = StateSucceeded
			base.Pending = false
			base.Reason = "fan policy was display-verified and saved"
		}
	case c.current == base.DesiredPolicy && c.current != PolicyUnknown && c.currentConfidence == "verified-live":
		base.State = StateNormal
		base.Pending = false
		base.Reason = "the last display-verified fan policy matches the desired policy"
	}
	return base
}
