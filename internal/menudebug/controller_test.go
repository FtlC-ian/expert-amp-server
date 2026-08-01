package menudebug

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

type fakeLease struct {
	acquired, released int
	busy, safety       bool
}

func (l *fakeLease) Acquire() bool {
	if l.busy {
		return false
	}
	l.acquired++
	return true
}
func (l *fakeLease) Release()         { l.released++ }
func (l *fakeLease) SafetyHold() bool { return l.safety }

func readyPrerequisites() Prerequisites {
	return Prerequisites{DebugEnabled: true, RecentProtocolStatus: true, ProtocolStandby: true, ProtocolRX: true, ChecksumValidDisplay: true, DisplayStandby: true, DisplayRX: true, HomeDisplay: true, DisplayGeneration: 4, StatusGeneration: 3}
}

func newDeterministicController(l *fakeLease) (*Controller, *time.Time) {
	now := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)
	c := NewController(l)
	c.now = func() time.Time { return now }
	c.newToken = func() (string, error) { return "test-token", nil }
	return c, &now
}

func arm(t *testing.T, c *Controller) (SessionView, string) {
	t.Helper()
	v, token, err := c.Arm(Acknowledgement, readyPrerequisites())
	if err != nil {
		t.Fatalf("Arm: %v", err)
	}
	return v, token
}

func TestArmRequiresEverySafetyPrerequisite(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Prerequisites)
		want   string
	}{
		{"debug disabled", func(p *Prerequisites) { p.DebugEnabled = false }, "disabled"},
		{"stale status", func(p *Prerequisites) { p.RecentProtocolStatus = false }, "fresh protocol"},
		{"operate", func(p *Prerequisites) { p.ProtocolStandby = false }, "STANDBY/RX"},
		{"protocol tx", func(p *Prerequisites) { p.ProtocolRX = false }, "STANDBY/RX"},
		{"invalid lcd", func(p *Prerequisites) { p.ChecksumValidDisplay = false }, "checksum-valid"},
		{"lcd operate", func(p *Prerequisites) { p.DisplayStandby = false }, "checksum-valid"},
		{"lcd tx", func(p *Prerequisites) { p.DisplayRX = false }, "checksum-valid"},
		{"not home", func(p *Prerequisites) { p.HomeDisplay = false }, "checksum-valid"},
		{"automatic fan", func(p *Prerequisites) { p.AutomaticFanPolicyActive = true }, "fan control"},
		{"manual fan", func(p *Prerequisites) { p.ManualFanControlActive = true }, "fan control"},
		{"safety armed", func(p *Prerequisites) { p.OvertemperatureArmed = true }, "disarmed"},
		{"no generations", func(p *Prerequisites) { p.DisplayGeneration = 0 }, "generations"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newDeterministicController(&fakeLease{})
			p := readyPrerequisites()
			tc.mutate(&p)
			if _, _, err := c.Arm(Acknowledgement, p); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Arm error = %v, want %q", err, tc.want)
			}
		})
	}
	c, _ := newDeterministicController(&fakeLease{})
	if _, _, err := c.Arm("yes", readyPrerequisites()); err == nil || !strings.Contains(err.Error(), "exact safety acknowledgement") {
		t.Fatalf("ack error = %v", err)
	}
}

func TestArmClearsReportsFromPriorSession(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	c.reports = []CapabilityReport{{Capability: CapabilityFan}}

	if _, _, err := c.Arm(Acknowledgement, readyPrerequisites()); err != nil {
		t.Fatal(err)
	}
	if len(c.reports) != 0 {
		t.Fatalf("new session retained prior reports: %+v", c.reports)
	}
}

func TestTokenAndRevisionPreventReplay(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	if _, err := c.Begin("wrong", v.Revision, CapabilityFan); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("wrong token error = %v", err)
	}
	next, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := c.InstallPlan(token, v.Revision, simplePlan(CapabilityFan, "NORMAL", "CONTEST")); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("replay error = %v", err)
	}
	if next.Revision == v.Revision {
		t.Fatal("successful mutation did not advance revision")
	}
}

func TestExpiryFailsClosedAndReleasesLease(t *testing.T) {
	l := &fakeLease{}
	c, now := newDeterministicController(l)
	c.lifetime = time.Minute
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if _, err := c.InstallPlan(token, v.Revision, simplePlan(CapabilityFan, "NORMAL", "CONTEST")); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expiry error = %v", err)
	}
	if c.session.phase != PhaseExpired || l.released != 1 {
		t.Fatalf("session=%+v lease=%+v", c.session, l)
	}
}

func TestFanAndBankRunAsSeparateReversibleCapabilities(t *testing.T) {
	l := &fakeLease{}
	c, _ := newDeterministicController(l)
	v, token := arm(t, c)
	v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
	if len(v.Completed) != 1 || v.Completed[0] != CapabilityFan || v.Phase != PhaseArmed {
		t.Fatalf("after fan: %+v", v)
	}
	v = completeCapability(t, c, token, v, simplePlan(CapabilityBank, "A", "B"))
	if len(v.Completed) != 2 || v.Completed[1] != CapabilityBank || l.acquired != 2 || l.released != 2 || c.session.actionBudget != 36 {
		t.Fatalf("after bank: view=%+v lease=%+v", v, l)
	}
	report := c.Report("EXPERT 1.3K-FA", "1.2.3", "v0.3.2")
	if len(report.Capabilities) != 2 || !report.Capabilities[0].AppliedVerified || !report.Capabilities[0].RestoreVerified {
		t.Fatalf("report=%+v", report)
	}
}

func TestCompletedCapabilityDoesNotAuthorizeNextCapabilityRestore(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
	plan := simplePlan(CapabilityBank, "A", "B")
	var err error
	v, err = c.Begin(token, v.Revision, CapabilityBank)
	if err != nil {
		t.Fatal(err)
	}
	base := c.session.lastEvidenceGen
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, Evidence{Generation: base + 1, Fingerprint: "bank-entry", Candidate: CapabilityBank})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityBank, plan.OriginalValue, false))
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.InstallPlan(token, v.Revision, plan)
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.BeginApply(token, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = c.AuthorizeNext(token, v.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityBank, plan.OriginalValue, false))
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveActionResult(token, auth.Revision, capabilityEvidence(base+3, plan.Apply[0].ExpectedFingerprint, CapabilityBank, plan.CandidateValue, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.BeginRestore(token, v.Revision); err == nil || (!strings.Contains(err.Error(), "not been user verified") && !strings.Contains(err.Error(), "not ready")) {
		t.Fatalf("BeginRestore error = %v", err)
	}
}

func completeCapability(t *testing.T, c *Controller, token string, v SessionView, plan Plan) SessionView {
	t.Helper()
	var err error
	v, err = c.Begin(token, v.Revision, plan.Capability)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	base := c.session.lastEvidenceGen
	discovery := Evidence{Generation: base + 1, Fingerprint: "candidate-entry", Candidate: plan.Capability}
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, discovery)
	if err != nil {
		t.Fatalf("Authorize discovery: %v", err)
	}
	current := capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, plan.Capability, plan.OriginalValue, false)
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, current)
	if err != nil {
		t.Fatalf("Observe discovery: %v", err)
	}
	v, err = c.InstallPlan(token, v.Revision, plan)
	if err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	if len(v.PlanSummary) != len(plan.Apply)+len(plan.Restore) || v.OriginalValue != plan.OriginalValue || v.CandidateValue != plan.CandidateValue {
		t.Fatalf("plan was not exposed for review: %+v", v)
	}
	v, err = c.BeginApply(token, v.Revision)
	if err != nil {
		t.Fatalf("BeginApply: %v", err)
	}
	generation := base + 2
	for index, step := range plan.Apply {
		current = plannedEvidence(generation, step.FromFingerprint, plan.Capability, plan.OriginalValue, plan.CandidateValue, step, false)
		auth, err = c.AuthorizeNext(token, v.Revision, current)
		if err != nil {
			t.Fatalf("Authorize apply %d: %v", index, err)
		}
		generation++
		next := plannedResultEvidence(generation, step.ExpectedFingerprint, plan.Capability, plan.OriginalValue, plan.CandidateValue, step, index == len(plan.Apply)-1)
		if step.AllowStoringBeforeHome && index == len(plan.Apply)-1 {
			v, err = c.ObserveActionResult(token, auth.Revision, Evidence{Generation: generation, Fingerprint: "apply-storing", Kind: ScreenStoring})
			if err != nil || v.Phase != PhaseApplying {
				t.Fatalf("Observe apply storing: view=%+v err=%v", v, err)
			}
			generation++
			next.Generation = generation
			next.Fingerprint = "apply-home-after-storing"
			auth.Revision = v.Revision
		}
		v, err = c.ObserveActionResult(token, auth.Revision, next)
		if err != nil {
			t.Fatalf("Observe apply %d: %v", index, err)
		}
	}
	v, err = c.Confirm(token, v.Revision, true)
	if err != nil {
		t.Fatalf("Confirm apply: %v", err)
	}
	v, err = c.BeginRestore(token, v.Revision)
	if err != nil {
		t.Fatalf("BeginRestore: %v", err)
	}
	for index, step := range plan.Restore {
		if index == 0 {
			generation++
			current = Evidence{Generation: generation, Fingerprint: "fresh-restore-home", Kind: ScreenHome, StandbyHome: true}
		} else {
			current = plannedEvidence(generation, step.FromFingerprint, plan.Capability, plan.OriginalValue, plan.CandidateValue, step, true)
		}
		auth, err = c.AuthorizeNext(token, v.Revision, current)
		if err != nil {
			t.Fatalf("Authorize restore %d: %v", index, err)
		}
		generation++
		next := plannedResultEvidence(generation, step.ExpectedFingerprint, plan.Capability, plan.OriginalValue, plan.CandidateValue, step, index == len(plan.Restore)-1)
		if step.AllowStoringBeforeHome && index == len(plan.Restore)-1 {
			v, err = c.ObserveActionResult(token, auth.Revision, Evidence{Generation: generation, Fingerprint: "restore-storing", Kind: ScreenStoring})
			if err != nil || v.Phase != PhaseRestoring {
				t.Fatalf("Observe restore storing: view=%+v err=%v", v, err)
			}
			generation++
			next.Generation = generation
			next.Fingerprint = "restore-home-after-storing"
			auth.Revision = v.Revision
		}
		v, err = c.ObserveActionResult(token, auth.Revision, next)
		if err != nil {
			t.Fatalf("Observe restore %d: %v", index, err)
		}
	}
	v, err = c.Confirm(token, v.Revision, true)
	if err != nil {
		t.Fatalf("Confirm restore: %v", err)
	}
	return v
}

func simplePlan(capability Capability, original, candidate string) Plan {
	profile := "expert-1.3k-fa-first-series-fan-v1"
	if capability == CapabilityBank {
		profile = "expert-1.3k-fa-first-series-bank-ab-v1"
	}
	return Plan{Profile: profile, Capability: capability, OriginalValue: original, CandidateValue: candidate,
		Apply: []Step{
			{Action: ActionSet, Purpose: PurposeChangeValue, FromFingerprint: "apply-from", ExpectedFingerprint: "apply-changed"},
			{Action: ActionSet, Purpose: PurposeSave, FromFingerprint: "apply-changed", ExpectedFingerprint: "", ExpectedKind: ScreenHome, ExpectedStandbyHome: true, AllowStoringBeforeHome: true},
		},
		Restore: []Step{
			{Action: ActionSet, Purpose: PurposeEnterCandidate, FromFingerprint: "apply-home", ExpectedFingerprint: "restore-from"},
			{Action: ActionSet, Purpose: PurposeChangeValue, FromFingerprint: "restore-from", ExpectedFingerprint: "restore-changed"},
			{Action: ActionSet, Purpose: PurposeSave, FromFingerprint: "restore-changed", ExpectedFingerprint: "", ExpectedKind: ScreenHome, ExpectedStandbyHome: true, AllowStoringBeforeHome: true},
		}}
}

func TestReviewedPlanProfilesAreCapabilityBound(t *testing.T) {
	plan := simplePlan(CapabilityFan, "normal", "contest")
	plan.Capability = CapabilityBank
	if err := validatePlan(plan); err == nil || !strings.Contains(err.Error(), "reviewed server profile") {
		t.Fatalf("fan profile accepted for bank: %v", err)
	}
	plan = simplePlan(CapabilityBank, "A", "B")
	plan.Capability = CapabilityFan
	if err := validatePlan(plan); err == nil || !strings.Contains(err.Error(), "reviewed server profile") {
		t.Fatalf("bank profile accepted for fan: %v", err)
	}
}

func evidence(generation uint64, fingerprint string) Evidence {
	return Evidence{Generation: generation, Fingerprint: fingerprint}
}

func capabilityEvidence(generation uint64, fingerprint string, capability Capability, value string, save bool) Evidence {
	kind := ScreenFan
	if capability == CapabilityBank {
		kind = ScreenBank
	}
	selection := value
	if save {
		selection = "SAVE"
	}
	return Evidence{Generation: generation, Fingerprint: fingerprint, Kind: kind, Candidate: capability, Value: value, Selection: selection, SaveVisible: save}
}

func plannedEvidence(generation uint64, fingerprint string, capability Capability, original, candidate string, step Step, restoring bool) Evidence {
	value := original
	if restoring {
		value = candidate
	}
	if step.Purpose == PurposeSave {
		return capabilityEvidence(generation, fingerprint, capability, value, true)
	}
	if step.Purpose == PurposeEnterCandidate && restoring {
		return Evidence{Generation: generation, Fingerprint: fingerprint, Kind: ScreenHome, StandbyHome: true}
	}
	return capabilityEvidence(generation, fingerprint, capability, value, false)
}

func plannedResultEvidence(generation uint64, fingerprint string, capability Capability, original, candidate string, step Step, final bool) Evidence {
	if final {
		return Evidence{Generation: generation, Fingerprint: fingerprint, Kind: ScreenHome, StandbyHome: true}
	}
	value := candidate
	if strings.HasPrefix(fingerprint, "restore-") {
		value = original
	}
	return capabilityEvidence(generation, fingerprint, capability, value, step.Purpose == PurposeChangeValue)
}

func TestAbortAndFailureNeverAttemptBlindRecovery(t *testing.T) {
	t.Run("home", func(t *testing.T) {
		l := &fakeLease{}
		c, _ := newDeterministicController(l)
		v, token := arm(t, c)
		v, _ = c.Begin(token, v.Revision, CapabilityFan)
		v, err := c.Abort(token, v.Revision, true)
		if err != nil || v.MayBeInMenu || v.RecoveryInstructions != "" || l.released != 1 {
			t.Fatalf("view=%+v err=%v lease=%+v", v, err, l)
		}
	})
	t.Run("in menu", func(t *testing.T) {
		l := &fakeLease{}
		c, _ := newDeterministicController(l)
		v, token := arm(t, c)
		v, _ = c.Begin(token, v.Revision, CapabilityFan)
		v, err := c.Abort(token, v.Revision, false)
		if err != nil || !v.MayBeInMenu || v.RecoveryInstructions == "" || l.released != 1 {
			t.Fatalf("view=%+v err=%v lease=%+v", v, err, l)
		}
	})
	t.Run("unexpected display", func(t *testing.T) {
		l := &fakeLease{}
		c, _ := newDeterministicController(l)
		v, token := arm(t, c)
		plan := simplePlan(CapabilityFan, "NORMAL", "CONTEST")
		v, err := c.Begin(token, v.Revision, CapabilityFan)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, Evidence{Generation: 5, Fingerprint: "fan-entry", Candidate: CapabilityFan})
		if err != nil {
			t.Fatal(err)
		}
		v, err = c.ObserveDiscoveryResult(token, auth.Revision, capabilityEvidence(6, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
		if err != nil {
			t.Fatal(err)
		}
		v, err = c.InstallPlan(token, v.Revision, plan)
		if err != nil {
			t.Fatal(err)
		}
		v, err = c.BeginApply(token, v.Revision)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.AuthorizeNext(token, v.Revision, evidence(8, "wrong"))
		if err == nil || c.session.phase != PhaseFailed || !c.session.mayBeInMenu || l.released != 1 {
			t.Fatalf("err=%v session=%+v lease=%+v", err, c.session, l)
		}
	})
}

func TestDiscoveryIsBoundedAndSETRequiresServerCandidate(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	c.budget = 1
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityFan)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, evidence(5, "screen-a"))
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, evidence(6, "screen-b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthorizeDiscovery(token, v.Revision, ActionRight, evidence(6, "screen-b")); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget error=%v", err)
	}

	c, _ = newDeterministicController(&fakeLease{})
	v, token = arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityBank)
	if _, err = c.AuthorizeDiscovery(token, v.Revision, ActionSet, Evidence{Generation: 5, Fingerprint: "fan", Candidate: CapabilityFan}); err == nil || !strings.Contains(err.Error(), "server-identified") {
		t.Fatalf("candidate error=%v", err)
	}
}

func TestDiscoveryLoopFailsClosed(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityFan)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, evidence(5, "same-highlight-aware-fingerprint"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ObserveDiscoveryResult(token, auth.Revision, evidence(6, "same-highlight-aware-fingerprint")); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("loop error=%v", err)
	}
}

func TestPendingDiscoveryIgnoresRepeatedPolledScreen(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityFan)

	before := display.NewState()
	before.SetRow(0, "BEFORE")
	beforeScreen := Analyze(before)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, Evidence{Generation: 5, Fingerprint: beforeScreen.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}

	c.ObserveDisplay(before, 6, true, nil, nil)
	if c.session.phase != PhaseDiscovering || !c.session.discoveryPending {
		t.Fatalf("repeated screen ended pending discovery: %+v", c.session)
	}
	if c.session.revision != auth.Revision {
		t.Fatalf("repeated screen changed revision: got %d want %d", c.session.revision, auth.Revision)
	}

	after := display.NewState()
	after.SetRow(0, "AFTER")
	c.ObserveDisplay(after, 7, true, nil, nil)
	if c.session.phase != PhaseDiscovering || c.session.discoveryPending {
		t.Fatalf("distinct screen did not complete discovery action: %+v", c.session)
	}
}

func TestTopologyDisplayExitCompletesOnlyAtVerifiedHomeWithoutSave(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityBank)
	home := display.NewState()
	home.SetRow(6, "IN  BAND ANT BNK  CAT   OUT   SWR   TEMP")
	homeFingerprint := Analyze(home).Fingerprint
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, Evidence{Generation: 5, Fingerprint: homeFingerprint, Kind: ScreenHome})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, Evidence{Generation: 6, Fingerprint: "bank", Kind: ScreenBank, Candidate: CapabilityBank, Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err = c.AuthorizeTopologyExit(token, v.Revision, Evidence{Generation: 6, Fingerprint: "bank", Kind: ScreenBank, Candidate: CapabilityBank, Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Action != ActionDisplay || auth.Purpose != PurposeExitWithoutSave {
		t.Fatalf("exit authorization = %+v", auth)
	}

	rx, operate := false, false
	c.ObserveDisplay(home, 7, true, &rx, &operate)
	v, err = c.Current(token)
	if err != nil {
		t.Fatal(err)
	}
	if v.Phase != PhaseArmed || v.MayBeInMenu || len(v.Completed) != 1 || v.Completed[0] != CapabilityBank {
		t.Fatalf("no-save exit view = %+v", v)
	}
	report := c.Report("EXPERT 1.3K-FA", "", "test")
	if len(report.Capabilities) != 1 || len(report.Capabilities[0].ActionReceipts) < 2 {
		t.Fatalf("report = %+v", report)
	}
	last := report.Capabilities[0].ActionReceipts[len(report.Capabilities[0].ActionReceipts)-1]
	if last.Action != ActionDisplay || last.Purpose != PurposeExitWithoutSave {
		t.Fatalf("last receipt = %+v", last)
	}
	for _, receipt := range report.Capabilities[0].ActionReceipts {
		if receipt.Purpose == PurposeSave {
			t.Fatalf("topology exit unexpectedly recorded SAVE: %+v", report.Capabilities[0].ActionReceipts)
		}
	}
}

func TestDisplayEvidenceMustAdvance(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityFan)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, evidence(5, "screen-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ObserveDiscoveryResult(token, auth.Revision, evidence(5, "screen-b")); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("stale evidence error = %v", err)
	}
}

func TestReportJSONHasNoPrivateHostOrSessionFields(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	_ = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
	b, err := json.Marshal(c.Report("EXPERT 1.5K-FA", "fw", "server"))
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(b)
	for _, forbidden := range []string{"test-token", "token", "hostname", "callsign", "serialPort", "ipAddress", "/dev/tty"} {
		if strings.Contains(jsonText, forbidden) {
			t.Fatalf("report contains %q: %s", forbidden, jsonText)
		}
	}
	if !strings.Contains(jsonText, ReportSchemaVersion) {
		t.Fatalf("missing schema version: %s", jsonText)
	}
}
