package menudebug

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
	Acknowledgement = "I AM IN STANDBY AND WILL NOT TRANSMIT"
	defaultLifetime = 10 * time.Minute
	defaultBudget   = 32
)

type lease interface {
	Acquire() bool
	Release()
	SafetyHold() bool
}

type Controller struct {
	mu       sync.Mutex
	lease    lease
	now      func() time.Time
	newToken func() (string, error)
	lifetime time.Duration
	budget   int
	session  session
	reports  []CapabilityReport
	runtime  RuntimeSnapshot
}

type session struct {
	id, tokenHash                  string
	phase                          Phase
	capability                     Capability
	revision                       uint64
	expiresAt                      time.Time
	actionsAttempted, actionBudget int
	mayBeInMenu, leaseHeld         bool
	failure                        string
	completed                      []Capability
	seen                           map[string]struct{}
	discoveryPending               bool
	discoveryPurpose               Purpose
	pending                        *Step
	plan                           Plan
	stepIndex                      int
	lastEvidenceGen                uint64
	lastEvidenceFingerprint        string
	evidence                       []Evidence
	actions                        []Action
	actionReceipts                 []ActionReceipt
	transitions                    []TransitionReceipt
	verifications                  []VerificationReceipt
	applyVerified, restoreVerified bool
}

func NewController(l lease) *Controller {
	return &Controller{lease: l, now: time.Now, newToken: randomToken, lifetime: defaultLifetime, budget: defaultBudget, session: session{phase: PhaseIdle}}
}

// ObserveStatus records protocol-native status evidence. It never changes a
// session phase or sends an amplifier command.
func (c *Controller) ObserveStatus(status api.Status, generation uint64) {
	if c == nil || generation == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation < c.runtime.StatusGeneration {
		return
	}
	c.runtime.Status = status
	c.runtime.StatusGeneration = generation
	c.runtime.StatusObservedAt = c.now()
}

// ObserveDisplay records checksum/display evidence and closes a pending
// controller receipt only after a newer fingerprinted display arrives.
func (c *Controller) ObserveDisplay(state display.State, generation uint64, checksumValid bool, tx, operate *bool) {
	if c == nil || generation == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation < c.runtime.DisplayGeneration {
		return
	}
	screen := Analyze(state)
	evidence := Evidence{
		Generation:    generation,
		Fingerprint:   screen.Fingerprint,
		Kind:          screen.Kind,
		Rows:          rowsArray(screen.Rows),
		Selection:     screen.SelectedText,
		Candidate:     Capability(screen.Capability),
		Value:         observedScreenValue(screen),
		SaveVisible:   screen.SaveVisible,
		StandbyHome:   checksumValid && screen.Kind == ScreenHome && boolMatches(tx, false) && boolMatches(operate, false),
		ObservedAt:    c.now(),
		SetupTopology: screen.SetupTopology,
	}
	if screen.Capability == "fan,bank" {
		evidence.Candidate = ""
	}
	c.runtime.DisplayState = state
	c.runtime.DisplayGeneration = generation
	c.runtime.DisplayObservedAt = c.now()
	c.runtime.ChecksumValid = checksumValid
	c.runtime.DisplayTX = cloneBool(tx)
	c.runtime.DisplayOperate = cloneBool(operate)
	c.runtime.Screen = screen

	if !checksumValid || c.session.phase == PhaseIdle || generation <= c.session.lastEvidenceGen {
		return
	}
	if c.session.phase == PhaseDiscovering && c.session.discoveryPending {
		// Serial display polling can deliver another checksum-valid copy of the
		// pre-action screen before the amplifier applies the keypress. That is
		// not a loop and must not fail the session; keep waiting for a distinct
		// screen produced by the authorized action.
		if evidence.Fingerprint == c.session.lastEvidenceFingerprint {
			return
		}
		if err := c.acceptNewEvidenceLocked(evidence); err != nil {
			_ = c.failLocked(err.Error())
			return
		}
		if c.session.discoveryPurpose == PurposeExitWithoutSave {
			if !evidence.StandbyHome {
				_ = c.failLocked("DISPLAY no-save exit did not return to verified STANDBY/RX home")
				return
			}
			c.session.evidence = append(c.session.evidence, evidence)
			c.recordTransitionLocked(evidence)
			c.session.discoveryPending = false
			c.completeTopologyLocked(true)
			return
		}
		if _, exists := c.session.seen[evidence.Fingerprint]; exists {
			_ = c.failLocked("menu loop detected")
			return
		}
		c.session.seen[evidence.Fingerprint] = struct{}{}
		c.session.evidence = append(c.session.evidence, evidence)
		c.recordTransitionLocked(evidence)
		c.session.discoveryPending = false
		c.session.discoveryPurpose = ""
		c.bumpLocked()
		return
	}
	if c.session.pending != nil && (c.session.phase == PhaseApplying || c.session.phase == PhaseRestoring) {
		// As above, ignore repeated copies of the pre-action screen while a
		// planned keypress is in flight. The reviewed transition is validated
		// only after a distinct display arrives.
		if evidence.Fingerprint == c.session.lastEvidenceFingerprint {
			return
		}
		if err := c.acceptNewEvidenceLocked(evidence); err != nil {
			_ = c.failLocked(err.Error())
			return
		}
		if c.session.pending.AllowStoringBeforeHome && evidence.Kind == ScreenStoring {
			c.session.evidence = append(c.session.evidence, evidence)
			c.recordTransitionLocked(evidence)
			c.bumpLocked()
			return
		}
		if evidence.Fingerprint != c.session.pending.ExpectedFingerprint {
			if c.session.pending.ExpectedFingerprint != "" {
				_ = c.failLocked("unexpected display after planned action")
				return
			}
		}
		if err := matchesStepExpectation(*c.session.pending, evidence); err != nil {
			_ = c.failLocked(err.Error())
			return
		}
		if err := c.validateObservedTransitionLocked(evidence); err != nil {
			_ = c.failLocked(err.Error())
			return
		}
		c.session.evidence = append(c.session.evidence, evidence)
		c.recordTransitionLocked(evidence)
		c.session.pending = nil
		c.session.stepIndex++
		if c.session.phase == PhaseApplying && c.session.stepIndex < len(c.session.plan.Apply) {
			c.session.plan.Apply[c.session.stepIndex].FromFingerprint = evidence.Fingerprint
		}
		if c.session.phase == PhaseRestoring && c.session.stepIndex < len(c.session.plan.Restore) {
			c.session.plan.Restore[c.session.stepIndex].FromFingerprint = evidence.Fingerprint
		}
		c.bumpLocked()
		steps := c.session.plan.Apply
		awaiting := PhaseAwaitingApplyVerify
		if c.session.phase == PhaseRestoring {
			steps = c.session.plan.Restore
			awaiting = PhaseAwaitingRestoreVerify
		}
		if c.session.stepIndex == len(steps) {
			if !evidence.StandbyHome {
				_ = c.failLocked("planned transaction did not finish on verified STANDBY/RX home")
				return
			}
			c.session.phase = awaiting
		}
	}
}

func rowsArray(rows []string) [8]string {
	var out [8]string
	copy(out[:], rows)
	return out
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (c *Controller) Runtime() RuntimeSnapshot {
	if c == nil {
		return RuntimeSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.runtime
	out.DisplayTX = cloneBool(out.DisplayTX)
	out.DisplayOperate = cloneBool(out.DisplayOperate)
	return out
}

func (c *Controller) Current(token string) (SessionView, error) {
	if c == nil {
		return SessionView{Phase: PhaseIdle}, errors.New("menu-debug controller unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.phase == PhaseIdle {
		return c.viewLocked(), nil
	}
	if err := c.authorizeTokenLocked(token); err != nil {
		return c.viewLocked(), err
	}
	return c.viewLocked(), nil
}

func (c *Controller) Tick(now time.Time) SessionView {
	if c == nil {
		return SessionView{Phase: PhaseIdle}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeLocked() && !now.Before(c.session.expiresAt) {
		c.session.phase = PhaseExpired
		c.session.failure = "menu-debug session expired"
		c.releaseLocked()
		c.bumpLocked()
	}
	return c.viewLocked()
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (c *Controller) Arm(ack string, pre Prerequisites) (SessionView, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeLocked() {
		return c.viewLocked(), "", errors.New("a menu-debug session is already active")
	}
	if strings.TrimSpace(ack) != Acknowledgement {
		return c.viewLocked(), "", errors.New("exact safety acknowledgement is required")
	}
	if err := validatePrerequisites(pre); err != nil {
		return c.viewLocked(), "", err
	}
	token, err := c.newToken()
	if err != nil {
		return c.viewLocked(), "", fmt.Errorf("create session token: %w", err)
	}
	now := c.now()
	lastFingerprint := ""
	if c.runtime.DisplayGeneration == pre.DisplayGeneration {
		lastFingerprint = c.runtime.Screen.Fingerprint
	}
	c.reports = nil
	c.session = session{id: tokenHash(token)[:16], tokenHash: tokenHash(token), phase: PhaseArmed, revision: 1, expiresAt: now.Add(c.lifetime), actionBudget: c.budget, seen: make(map[string]struct{}), lastEvidenceGen: pre.DisplayGeneration, lastEvidenceFingerprint: lastFingerprint}
	return c.viewLocked(), token, nil
}

func validatePrerequisites(p Prerequisites) error {
	switch {
	case !p.DebugEnabled:
		return errors.New("menu debug mode is disabled")
	case !p.RecentProtocolStatus:
		return errors.New("fresh protocol status is required")
	case !p.ProtocolStandby || !p.ProtocolRX:
		return errors.New("protocol must verify STANDBY/RX")
	case !p.ChecksumValidDisplay || !p.DisplayStandby || !p.DisplayRX || !p.HomeDisplay:
		return errors.New("checksum-valid STANDBY/RX home display is required")
	case p.AutomaticFanPolicyActive || p.ManualFanControlActive:
		return errors.New("fan control must be inactive")
	case p.OvertemperatureArmed:
		return errors.New("overtemperature actuation must be disarmed")
	case p.DisplayGeneration == 0 || p.StatusGeneration == 0:
		return errors.New("fresh display and status generations are required")
	default:
		return nil
	}
}

// ValidatePrerequisitesIgnoringFanControl applies every arm-time safety gate
// except the fan-control gate. The server uses this only to establish a
// unreachable provisional armed session before atomically removing an eligible
// completed Normal override.
func ValidatePrerequisitesIgnoringFanControl(p Prerequisites) error {
	p.AutomaticFanPolicyActive = false
	p.ManualFanControlActive = false
	return validatePrerequisites(p)
}

func (c *Controller) Begin(token string, revision uint64, capability Capability) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.phase != PhaseArmed {
		return c.viewLocked(), errors.New("session is not ready for a capability")
	}
	if capability != CapabilityFan && capability != CapabilityBank {
		return c.viewLocked(), errors.New("capability must be fan or bank")
	}
	if containsCapability(c.session.completed, capability) {
		return c.viewLocked(), errors.New("capability already completed in this session")
	}
	if c.lease == nil {
		return c.viewLocked(), errors.New("menu-debug actuation lease is unavailable")
	}
	if !c.lease.Acquire() {
		return c.viewLocked(), errors.New("amplifier actuation is busy")
	}
	c.session.leaseHeld = true
	c.session.capability = capability
	c.session.phase = PhaseDiscovering
	c.session.actionsAttempted = 0
	c.session.seen = make(map[string]struct{})
	c.session.discoveryPending = false
	c.session.discoveryPurpose = ""
	c.session.pending = nil
	c.session.plan = Plan{}
	c.session.stepIndex = 0
	c.session.evidence = nil
	c.session.actions = nil
	c.session.actionReceipts = nil
	c.session.transitions = nil
	c.session.verifications = nil
	c.session.mayBeInMenu = false
	c.session.failure = ""
	c.session.applyVerified = false
	c.session.restoreVerified = false
	c.bumpLocked()
	return c.viewLocked(), nil
}

// AuthorizeDiscovery permits only bounded selector movement, plus SET when the
// current server-derived evidence identifies the requested candidate menu.
func (c *Controller) AuthorizeDiscovery(token string, revision uint64, action Action, evidence Evidence) (ActionAuthorization, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return ActionAuthorization{}, err
	}
	if c.session.phase != PhaseDiscovering {
		return ActionAuthorization{}, errors.New("session is not discovering")
	}
	if c.session.discoveryPending {
		return ActionAuthorization{}, errors.New("previous discovery action still awaits display verification")
	}
	if err := c.verifyCurrentEvidenceLocked(evidence); err != nil {
		return ActionAuthorization{}, c.failLocked(err.Error())
	}
	if _, exists := c.session.seen[evidence.Fingerprint]; !exists {
		c.session.seen[evidence.Fingerprint] = struct{}{}
		c.session.evidence = append(c.session.evidence, evidence)
	}
	purpose := PurposeEnumerate
	if action == ActionSet {
		if evidence.Kind != ScreenHome && evidence.Candidate != c.session.capability {
			return ActionAuthorization{}, c.failLocked("SET is allowed only on a server-identified capability candidate")
		}
		purpose = PurposeEnterCandidate
		c.session.mayBeInMenu = true
	} else if action != ActionRight {
		return ActionAuthorization{}, c.failLocked("unsupported discovery action")
	}
	authorization, err := c.consumeActionLocked(action, purpose)
	if err == nil {
		c.session.discoveryPending = true
		c.session.discoveryPurpose = purpose
	}
	return authorization, err
}

// AuthorizeTopologyExit permits the model-reviewed serial DISPLAY no-save
// exit after read-only discovery. The caller must independently restrict this
// to a hardware profile where DISPLAY has been confirmed to return home
// without committing menu values.
func (c *Controller) AuthorizeTopologyExit(token string, revision uint64, evidence Evidence) (ActionAuthorization, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return ActionAuthorization{}, err
	}
	if c.session.phase != PhaseDiscovering || c.session.discoveryPending {
		return ActionAuthorization{}, errors.New("topology exit requires completed discovery evidence")
	}
	if err := c.verifyCurrentEvidenceLocked(evidence); err != nil {
		return ActionAuthorization{}, c.failLocked(err.Error())
	}
	if evidence.Candidate != c.session.capability || (evidence.Kind != ScreenFan && evidence.Kind != ScreenBank) {
		return ActionAuthorization{}, c.failLocked("DISPLAY no-save exit requires a classified active capability screen")
	}
	authorization, err := c.consumeActionLocked(ActionDisplay, PurposeExitWithoutSave)
	if err == nil {
		c.session.discoveryPending = true
		c.session.discoveryPurpose = PurposeExitWithoutSave
	}
	return authorization, err
}

// ObserveDiscoveryResult closes the receipt for one discovery keypress. The
// next keypress or plan installation remains blocked until a newer, distinct
// checksum-valid display observation has been supplied by the server walker.
func (c *Controller) ObserveDiscoveryResult(token string, revision uint64, evidence Evidence) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.phase != PhaseDiscovering || !c.session.discoveryPending {
		return c.viewLocked(), errors.New("no discovery action awaits display verification")
	}
	if err := c.acceptNewEvidenceLocked(evidence); err != nil {
		return c.viewLocked(), c.failLocked(err.Error())
	}
	if _, exists := c.session.seen[evidence.Fingerprint]; exists {
		return c.viewLocked(), c.failLocked("menu loop detected")
	}
	c.session.seen[evidence.Fingerprint] = struct{}{}
	c.session.evidence = append(c.session.evidence, evidence)
	c.recordTransitionLocked(evidence)
	c.session.discoveryPending = false
	c.session.discoveryPurpose = ""
	c.bumpLocked()
	return c.viewLocked(), nil
}

func (c *Controller) InstallPlan(token string, revision uint64, plan Plan) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.phase != PhaseDiscovering || plan.Capability != c.session.capability {
		return c.viewLocked(), errors.New("plan does not match active discovery")
	}
	if c.session.discoveryPending {
		return c.viewLocked(), errors.New("discovery action still awaits display verification")
	}
	if err := validatePlan(plan); err != nil {
		return c.viewLocked(), c.failLocked(err.Error())
	}
	if !modelMatches(plan.ExpectedModel, c.runtime.Status.ModelName) {
		return c.viewLocked(), c.failLocked("reviewed plan model does not match the connected amplifier")
	}
	if len(c.session.evidence) == 0 {
		return c.viewLocked(), c.failLocked("plan requires server-derived capability evidence")
	}
	latest := c.session.evidence[len(c.session.evidence)-1]
	if latest.Candidate != c.session.capability || plan.Apply[0].FromFingerprint != latest.Fingerprint {
		return c.viewLocked(), c.failLocked("plan is not bound to the latest server-derived capability screen")
	}
	if (c.session.capability == CapabilityFan && latest.Kind != ScreenFan) || (c.session.capability == CapabilityBank && latest.Kind != ScreenBank) {
		return c.viewLocked(), c.failLocked("plan is not bound to a classified capability screen")
	}
	if latest.Value != plan.OriginalValue {
		return c.viewLocked(), c.failLocked("plan original value does not match classified display evidence")
	}
	if len(plan.DiscoverySetupWaypoints) != 0 && !matchesDiscoverySetupWaypoints(c.session.evidence, plan.DiscoverySetupTopology, plan.DiscoverySetupWaypoints) {
		return c.viewLocked(), c.failLocked("reviewed profile does not match the observed setup-menu topology")
	}
	c.session.plan = plan
	if plan.Profile == "expert-1.3k-fa-first-series-bank-ab-v1" && c.session.actionBudget < 36 {
		c.session.actionBudget = 36
	}
	c.session.phase = PhasePlanReady
	c.session.stepIndex = 0
	c.session.pending = nil
	c.bumpLocked()
	return c.viewLocked(), nil
}

func matchesDiscoverySetupWaypoints(evidence []Evidence, expectedTopology string, expected []string) bool {
	actual := make([]string, 0, len(expected))
	for _, item := range evidence {
		if item.Kind == ScreenSetup {
			if expectedTopology != "" && item.SetupTopology != expectedTopology {
				return false
			}
			actual = append(actual, strings.TrimSpace(item.Selection))
		}
	}
	if len(actual) != len(expected) {
		return false
	}
	for index, want := range expected {
		if strings.HasPrefix(want, "~") {
			if !strings.Contains(strings.ToUpper(actual[index]), strings.ToUpper(strings.TrimPrefix(want, "~"))) {
				return false
			}
		} else if !strings.EqualFold(actual[index], want) {
			return false
		}
	}
	return true
}

func validatePlan(p Plan) error {
	type reviewedProfile struct {
		capability Capability
		model      string
	}
	profile := map[string]reviewedProfile{
		"expert-1.3k-fa-first-series-fan-v1":     {CapabilityFan, "EXPERT 1.3K-FA"},
		"expert-1.5k-fa-second-series-fan-v1":    {CapabilityFan, "EXPERT 1.5K-FA"},
		"expert-1.3k-fa-first-series-bank-ab-v1": {CapabilityBank, "EXPERT 1.3K-FA"},
	}[p.Profile]
	if profile.capability == "" || profile.capability != p.Capability || !modelMatches(profile.model, p.ExpectedModel) {
		return errors.New("plan does not use a reviewed server profile")
	}
	if strings.TrimSpace(p.OriginalValue) == "" || strings.TrimSpace(p.CandidateValue) == "" || p.OriginalValue == p.CandidateValue {
		return errors.New("plan must preserve distinct original and candidate values")
	}
	if len(p.Apply) == 0 || len(p.Restore) == 0 {
		return errors.New("plan requires separate apply and restore steps")
	}
	for _, steps := range [][]Step{p.Apply, p.Restore} {
		hasChange := false
		for _, s := range steps {
			if s.Action != ActionRight && s.Action != ActionSet {
				return errors.New("plan contains unsupported action")
			}
			if s.ExpectedFingerprint == "" && s.ExpectedKind == "" && !s.ExpectedStandbyHome {
				return errors.New("every plan step requires an exact fingerprint or reviewed semantic expectation")
			}
			switch s.Purpose {
			case PurposeEnumerate:
				if s.Action != ActionRight {
					return errors.New("enumeration must use RIGHT")
				}
			case PurposeEnterCandidate, PurposeChangeValue, PurposeSave:
				if s.Action != ActionSet {
					return errors.New("menu entry, value change, and save must use SET")
				}
			default:
				return errors.New("plan contains unsupported action purpose")
			}
			if s.Purpose == PurposeChangeValue {
				hasChange = true
			}
		}
		if !hasChange || steps[len(steps)-1].Purpose != PurposeSave {
			return errors.New("apply and restore must each change one value and finish with SAVE")
		}
	}
	return nil
}

func (c *Controller) BeginApply(token string, revision uint64) (SessionView, error) {
	return c.beginPlan(token, revision, PhasePlanReady, PhaseApplying)
}
func (c *Controller) BeginRestore(token string, revision uint64) (SessionView, error) {
	return c.beginPlan(token, revision, PhaseAwaitingApplyVerify, PhaseRestoring)
}

func (c *Controller) beginPlan(token string, revision uint64, from, to Phase) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.phase != from {
		return c.viewLocked(), fmt.Errorf("session is not ready for %s", to)
	}
	if !modelMatches(c.session.plan.ExpectedModel, c.runtime.Status.ModelName) {
		return c.viewLocked(), c.failLocked("connected amplifier model changed after plan discovery")
	}
	if to == PhaseRestoring && !c.session.applyVerified {
		return c.viewLocked(), errors.New("applied change has not been user verified")
	}
	c.session.phase = to
	c.session.stepIndex = 0
	c.session.pending = nil
	freshHome := len(c.session.evidence) > 0 && c.session.evidence[len(c.session.evidence)-1].StandbyHome
	if !freshHome {
		freshHome = c.runtime.Screen.Kind == ScreenHome && c.runtime.ChecksumValid && boolMatches(c.runtime.DisplayTX, false) && boolMatches(c.runtime.DisplayOperate, false)
	}
	if to == PhaseRestoring && !freshHome {
		return c.viewLocked(), c.failLocked("restore requires a fresh verified STANDBY/RX home display")
	}
	c.bumpLocked()
	return c.viewLocked(), nil
}

func (c *Controller) AuthorizeNext(token string, revision uint64, evidence Evidence) (ActionAuthorization, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return ActionAuthorization{}, err
	}
	var steps []Step
	if c.session.phase == PhaseApplying {
		steps = c.session.plan.Apply
	} else if c.session.phase == PhaseRestoring {
		steps = c.session.plan.Restore
	} else {
		return ActionAuthorization{}, errors.New("session is not applying or restoring")
	}
	if c.session.pending != nil {
		return ActionAuthorization{}, errors.New("previous action still awaits display verification")
	}
	if c.session.stepIndex >= len(steps) {
		return ActionAuthorization{}, errors.New("all planned actions are complete")
	}
	if !modelMatches(c.session.plan.ExpectedModel, c.runtime.Status.ModelName) {
		return ActionAuthorization{}, c.failLocked("connected amplifier model changed after plan discovery")
	}
	if err := c.verifyCurrentEvidenceLocked(evidence); err != nil {
		return ActionAuthorization{}, c.failLocked(err.Error())
	}
	step := steps[c.session.stepIndex]
	if c.session.phase == PhaseRestoring && c.session.stepIndex == 0 && !evidence.StandbyHome {
		return ActionAuthorization{}, c.failLocked("restore entry requires current verified STANDBY/RX home evidence")
	}
	semanticRestoreEntry := c.session.phase == PhaseRestoring && c.session.stepIndex == 0
	if !semanticRestoreEntry && step.FromFingerprint != "" && evidence.Fingerprint != step.FromFingerprint {
		return ActionAuthorization{}, c.failLocked("current display does not match the frozen plan")
	}
	if step.Purpose == PurposeSave && (!evidence.SaveVisible || !strings.Contains(strings.ToUpper(evidence.Selection), "SAVE")) {
		return ActionAuthorization{}, c.failLocked("SAVE is allowed only from classified selected SAVE evidence")
	}
	if step.Purpose == PurposeChangeValue {
		if (c.session.capability == CapabilityFan && evidence.Kind != ScreenFan) || (c.session.capability == CapabilityBank && evidence.Kind != ScreenBank) {
			return ActionAuthorization{}, c.failLocked("value change is not on the classified capability screen")
		}
	}
	c.session.pending = &step
	c.session.evidence = append(c.session.evidence, evidence)
	if step.Action == ActionSet {
		c.session.mayBeInMenu = true
	}
	return c.consumeActionLocked(step.Action, step.Purpose)
}

func (c *Controller) ObserveActionResult(token string, revision uint64, evidence Evidence) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.pending == nil {
		return c.viewLocked(), errors.New("no planned action awaits verification")
	}
	if err := c.acceptNewEvidenceLocked(evidence); err != nil {
		return c.viewLocked(), c.failLocked(err.Error())
	}
	if c.session.pending.AllowStoringBeforeHome && evidence.Kind == ScreenStoring {
		c.session.evidence = append(c.session.evidence, evidence)
		c.recordTransitionLocked(evidence)
		c.bumpLocked()
		return c.viewLocked(), nil
	}
	if c.session.pending.ExpectedFingerprint != "" && evidence.Fingerprint != c.session.pending.ExpectedFingerprint {
		return c.viewLocked(), c.failLocked("unexpected display after planned action")
	}
	if err := matchesStepExpectation(*c.session.pending, evidence); err != nil {
		return c.viewLocked(), c.failLocked(err.Error())
	}
	if err := c.validateObservedTransitionLocked(evidence); err != nil {
		return c.viewLocked(), c.failLocked(err.Error())
	}
	c.session.evidence = append(c.session.evidence, evidence)
	c.recordTransitionLocked(evidence)
	c.session.pending = nil
	c.session.stepIndex++
	if c.session.phase == PhaseApplying && c.session.stepIndex < len(c.session.plan.Apply) {
		c.session.plan.Apply[c.session.stepIndex].FromFingerprint = evidence.Fingerprint
	}
	if c.session.phase == PhaseRestoring && c.session.stepIndex < len(c.session.plan.Restore) {
		c.session.plan.Restore[c.session.stepIndex].FromFingerprint = evidence.Fingerprint
	}
	c.bumpLocked()
	steps := c.session.plan.Apply
	awaiting := PhaseAwaitingApplyVerify
	if c.session.phase == PhaseRestoring {
		steps = c.session.plan.Restore
		awaiting = PhaseAwaitingRestoreVerify
	}
	if c.session.stepIndex == len(steps) {
		if !evidence.StandbyHome {
			return c.viewLocked(), c.failLocked("planned transaction did not finish on verified STANDBY/RX home")
		}
		c.session.phase = awaiting
	}
	return c.viewLocked(), nil
}

func (c *Controller) Confirm(token string, revision uint64, worked bool) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if !worked {
		c.session.verifications = append(c.session.verifications, VerificationReceipt{Phase: c.session.phase, Verified: false, At: c.now().UTC()})
		return c.viewLocked(), c.failLocked("operator verification failed")
	}
	c.session.verifications = append(c.session.verifications, VerificationReceipt{Phase: c.session.phase, Verified: true, At: c.now().UTC()})
	switch c.session.phase {
	case PhaseAwaitingApplyVerify:
		c.session.applyVerified = true
	case PhaseAwaitingRestoreVerify:
		c.session.restoreVerified = true
		c.session.completed = append(c.session.completed, c.session.capability)
		c.reports = append(c.reports, c.capabilityReportLocked())
		c.releaseLocked()
		c.session.phase = PhaseArmed
		c.session.capability = ""
		c.session.plan = Plan{}
		c.session.mayBeInMenu = false
	default:
		return c.viewLocked(), errors.New("session is not awaiting operator verification")
	}
	c.bumpLocked()
	return c.viewLocked(), nil
}

// CompleteTopology records a read-only capability observation when no reviewed
// action profile exists. It releases the lease and never authorizes a value
// change or SAVE.
func (c *Controller) CompleteTopology(token string, revision uint64) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.phase != PhaseDiscovering || c.session.discoveryPending || len(c.session.evidence) == 0 {
		return c.viewLocked(), errors.New("no completed topology observation is available")
	}
	latest := c.session.evidence[len(c.session.evidence)-1]
	if latest.Candidate != c.session.capability {
		return c.viewLocked(), c.failLocked("topology observation does not match the active capability")
	}
	c.completeTopologyLocked(false)
	return c.viewLocked(), nil
}

func (c *Controller) completeTopologyLocked(atVerifiedHome bool) {
	original := ""
	for i := len(c.session.evidence) - 1; i >= 0; i-- {
		if c.session.evidence[i].Candidate == c.session.capability {
			original = c.session.evidence[i].Value
			break
		}
	}
	c.reports = append(c.reports, CapabilityReport{Profile: "topology-only", Capability: c.session.capability, OriginalValue: original, Evidence: append([]Evidence(nil), c.session.evidence...), Actions: append([]Action(nil), c.session.actions...), ActionReceipts: append([]ActionReceipt(nil), c.session.actionReceipts...), Transitions: append([]TransitionReceipt(nil), c.session.transitions...)})
	c.session.completed = append(c.session.completed, c.session.capability)
	c.releaseLocked()
	c.session.phase = PhaseArmed
	c.session.capability = ""
	c.session.plan = Plan{}
	c.session.discoveryPurpose = ""
	c.session.mayBeInMenu = !atVerifiedHome
	c.bumpLocked()
}

func (c *Controller) Complete(token string, revision uint64) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	if c.session.phase != PhaseArmed || c.session.leaseHeld {
		return c.viewLocked(), errors.New("menu-debug session is not ready to complete")
	}
	c.session.phase = PhaseComplete
	c.bumpLocked()
	return c.viewLocked(), nil
}

func (c *Controller) Abort(token string, revision uint64, atVerifiedHome bool) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	c.session.phase = PhaseAborted
	if !atVerifiedHome {
		c.session.mayBeInMenu = true
		c.session.failure = "aborted away from verified home; physical-panel recovery required"
	}
	c.releaseLocked()
	c.bumpLocked()
	return c.viewLocked(), nil
}

func (c *Controller) Fail(token string, revision uint64, reason string) (SessionView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorizeLocked(token, revision); err != nil {
		return c.viewLocked(), err
	}
	return c.viewLocked(), c.failLocked(strings.TrimSpace(reason))
}

func (c *Controller) consumeActionLocked(action Action, purpose Purpose) (ActionAuthorization, error) {
	if c.lease != nil && c.lease.SafetyHold() {
		return ActionAuthorization{}, c.failLocked("overtemperature safety preempted menu debug")
	}
	c.session.actionsAttempted++
	if c.session.actionsAttempted > c.session.actionBudget {
		return ActionAuthorization{}, c.failLocked("menu-debug action budget exhausted")
	}
	c.session.actions = append(c.session.actions, action)
	c.session.actionReceipts = append(c.session.actionReceipts, ActionReceipt{Action: action, Purpose: purpose, At: c.now().UTC()})
	c.bumpLocked()
	return ActionAuthorization{Action: action, Purpose: purpose, Revision: c.session.revision, ExpectedModel: c.session.plan.ExpectedModel}, nil
}

func modelMatches(expected, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	return expected != "" && strings.EqualFold(expected, actual)
}

func (c *Controller) verifyCurrentEvidenceLocked(evidence Evidence) error {
	if evidence.Generation == 0 || evidence.Fingerprint == "" {
		return errors.New("fresh fingerprinted display evidence is required")
	}
	if evidence.Generation < c.session.lastEvidenceGen {
		return errors.New("display evidence is older than the previous observation")
	}
	if evidence.Generation == c.session.lastEvidenceGen {
		if c.session.lastEvidenceFingerprint == "" || evidence.Fingerprint != c.session.lastEvidenceFingerprint {
			return errors.New("display evidence does not match the latest verified observation")
		}
		return nil
	}
	c.session.lastEvidenceGen = evidence.Generation
	c.session.lastEvidenceFingerprint = evidence.Fingerprint
	return nil
}

func (c *Controller) acceptNewEvidenceLocked(evidence Evidence) error {
	if evidence.Generation == 0 || evidence.Fingerprint == "" {
		return errors.New("fresh fingerprinted display evidence is required")
	}
	if evidence.Generation <= c.session.lastEvidenceGen {
		return errors.New("display evidence must be newer than the previous observation")
	}
	c.session.lastEvidenceGen = evidence.Generation
	c.session.lastEvidenceFingerprint = evidence.Fingerprint
	return nil
}

func (c *Controller) authorizeLocked(token string, revision uint64) error {
	if c.session.phase == PhaseIdle {
		return errors.New("menu-debug session is not armed")
	}
	if !c.now().Before(c.session.expiresAt) {
		c.session.phase = PhaseExpired
		c.session.failure = "menu-debug session expired"
		c.releaseLocked()
		c.bumpLocked()
		return errors.New(c.session.failure)
	}
	if err := c.authorizeTokenLocked(token); err != nil {
		return err
	}
	if revision != c.session.revision {
		return errors.New("stale or replayed menu-debug session revision")
	}
	return nil
}

func (c *Controller) authorizeTokenLocked(token string) error {
	if !c.now().Before(c.session.expiresAt) {
		c.session.phase = PhaseExpired
		c.session.failure = "menu-debug session expired"
		c.releaseLocked()
		c.bumpLocked()
		return errors.New(c.session.failure)
	}
	want, got := []byte(c.session.tokenHash), []byte(tokenHash(token))
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got) != 1 {
		return errors.New("invalid menu-debug session token")
	}
	return nil
}

func (c *Controller) failLocked(reason string) error {
	if reason == "" {
		reason = "menu-debug transaction failed"
	}
	wasNavigating := c.session.phase == PhaseDiscovering ||
		c.session.phase == PhaseApplying ||
		c.session.phase == PhaseRestoring
	c.session.phase = PhaseFailed
	c.session.failure = reason
	c.session.mayBeInMenu = c.session.mayBeInMenu || c.session.actionsAttempted > 0 || wasNavigating
	c.releaseLocked()
	c.bumpLocked()
	return errors.New(reason)
}

func (c *Controller) releaseLocked() {
	if c.session.leaseHeld && c.lease != nil {
		c.lease.Release()
	}
	c.session.leaseHeld = false
}
func (c *Controller) bumpLocked() { c.session.revision++ }
func (c *Controller) activeLocked() bool {
	switch c.session.phase {
	case PhaseIdle, PhaseComplete, PhaseFailed, PhaseExpired, PhaseAborted:
		return false
	}
	return true
}
func containsCapability(v []Capability, target Capability) bool {
	for _, x := range v {
		if x == target {
			return true
		}
	}
	return false
}

func (c *Controller) viewLocked() SessionView {
	v := SessionView{ID: c.session.id, Phase: c.session.phase, Capability: c.session.capability, Revision: c.session.revision, ActionsAttempted: c.session.actionsAttempted, ActionBudget: c.session.actionBudget, MayBeInMenu: c.session.mayBeInMenu, Failure: c.session.failure, Completed: append([]Capability(nil), c.session.completed...), PlanProfile: c.session.plan.Profile, OriginalValue: c.session.plan.OriginalValue, CandidateValue: c.session.plan.CandidateValue}
	if c.session.phase == PhaseApplying {
		v.StepNumber, v.TotalSteps = c.session.stepIndex+1, len(c.session.plan.Apply)
	} else if c.session.phase == PhaseRestoring {
		v.StepNumber, v.TotalSteps = c.session.stepIndex+1, len(c.session.plan.Restore)
	}
	for index, step := range c.session.plan.Apply {
		v.PlanSummary = append(v.PlanSummary, summarizeStep("apply", index+1, step))
	}
	for index, step := range c.session.plan.Restore {
		v.PlanSummary = append(v.PlanSummary, summarizeStep("restore", index+1, step))
	}
	if len(c.session.evidence) > 0 {
		last := c.session.evidence[len(c.session.evidence)-1]
		v.LastVerifiedScreen = string(last.Kind) + " " + last.Fingerprint
	}
	if !c.session.expiresAt.IsZero() {
		v.ExpiresAt = c.session.expiresAt.UTC().Format(time.RFC3339Nano)
	}
	if v.MayBeInMenu {
		v.RecoveryInstructions = "Stop transmitting, keep the amplifier in STANDBY, and use the physical front panel to return to the home screen; no blind recovery or OPERATE restoration is attempted."
	}
	return v
}

func summarizeStep(transaction string, number int, step Step) PlanStepSummary {
	expected := string(step.ExpectedKind)
	if step.ExpectedSelection != "" {
		expected += " selection " + step.ExpectedSelection
	} else if step.ExpectedSelectionContains != "" {
		expected += " selection containing " + step.ExpectedSelectionContains
	}
	if step.ExpectedValue != "" {
		expected += " value " + step.ExpectedValue
	}
	if step.ExpectedStandbyHome {
		expected = "verified STANDBY/RX home"
		if step.AllowStoringBeforeHome {
			expected = "optional STORING DATA receipt, then " + expected
		}
	}
	return PlanStepSummary{Transaction: transaction, Number: number, Action: step.Action, Purpose: step.Purpose, Expected: strings.TrimSpace(expected)}
}

func (c *Controller) capabilityReportLocked() CapabilityReport {
	return CapabilityReport{Profile: c.session.plan.Profile, Capability: c.session.capability, OriginalValue: c.session.plan.OriginalValue, CandidateValue: c.session.plan.CandidateValue, AppliedVerified: c.session.applyVerified, RestoreVerified: c.session.restoreVerified, Evidence: append([]Evidence(nil), c.session.evidence...), Actions: append([]Action(nil), c.session.actions...), ActionReceipts: append([]ActionReceipt(nil), c.session.actionReceipts...), Transitions: append([]TransitionReceipt(nil), c.session.transitions...), Verifications: append([]VerificationReceipt(nil), c.session.verifications...)}
}

func (c *Controller) validateObservedTransitionLocked(evidence Evidence) error {
	if c.session.pending == nil || c.session.pending.Purpose != PurposeChangeValue {
		return nil
	}
	want := c.session.plan.CandidateValue
	if c.session.phase == PhaseRestoring {
		want = c.session.plan.OriginalValue
	}
	if evidence.Value != want {
		return errors.New("observed value does not match the frozen plan transition")
	}
	return nil
}

func matchesStepExpectation(step Step, evidence Evidence) error {
	if step.ExpectedStandbyHome && !evidence.StandbyHome {
		return errors.New("expected a verified STANDBY/RX home display")
	}
	if step.ExpectedKind != "" && evidence.Kind != step.ExpectedKind {
		return errors.New("observed screen kind does not match the reviewed plan")
	}
	if step.ExpectedCapability != "" && evidence.Candidate != step.ExpectedCapability {
		return errors.New("observed capability does not match the reviewed plan")
	}
	if step.ExpectedValue != "" && !strings.EqualFold(evidence.Value, step.ExpectedValue) {
		return errors.New("observed value does not match the reviewed plan")
	}
	if step.ExpectedSelection != "" && !strings.EqualFold(strings.TrimSpace(evidence.Selection), step.ExpectedSelection) {
		return errors.New("observed selection does not match the reviewed plan")
	}
	if step.ExpectedSelectionContains != "" && !strings.Contains(strings.ToUpper(evidence.Selection), strings.ToUpper(step.ExpectedSelectionContains)) {
		return errors.New("observed selection is outside the reviewed plan")
	}
	if step.ExpectedSaveVisible && !evidence.SaveVisible {
		return errors.New("expected SAVE evidence was not visible")
	}
	return nil
}

func (c *Controller) recordTransitionLocked(evidence Evidence) {
	from := ""
	if count := len(c.session.transitions); count > 0 {
		from = c.session.transitions[count-1].ToFingerprint
	}
	c.session.transitions = append(c.session.transitions, TransitionReceipt{FromFingerprint: from, ToFingerprint: evidence.Fingerprint, Kind: evidence.Kind, At: c.now().UTC()})
}

func boolMatches(value *bool, want bool) bool { return value != nil && *value == want }

func observedScreenValue(screen ScreenObservation) string {
	if screen.ActiveValue != "" {
		return screen.ActiveValue
	}
	return screen.SelectedValue
}

func (c *Controller) Report(model, firmware, serverVersion string) Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Report{SchemaVersion: ReportSchemaVersion, Model: strings.TrimSpace(model), Firmware: strings.TrimSpace(firmware), ServerVersion: strings.TrimSpace(serverVersion), Capabilities: append([]CapabilityReport(nil), c.reports...)}
}

var _ lease = (transport.LeaseButtonTransport)(nil)
