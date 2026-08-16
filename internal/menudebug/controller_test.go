package menudebug

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
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
	c.ObserveStatus(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 1)
	c.runtime.DisplaySerialSessionGeneration = 1
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

func TestActiveSessionLatchesTransientTXObservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		observe func(*Controller, *bool, *bool)
	}{
		{
			name: "status",
			observe: func(c *Controller, tx, rx *bool) {
				c.ObserveStatus(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA", TX: tx}}, 2)
				c.ObserveStatus(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA", TX: rx}}, 3)
			},
		},
		{
			name: "display",
			observe: func(c *Controller, tx, rx *bool) {
				operate := false
				c.ObserveDisplay(display.State{}, 5, true, tx, &operate)
				c.ObserveDisplay(display.State{}, 6, true, rx, &operate)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newDeterministicController(&fakeLease{})
			view, token := arm(t, c)
			view, err := c.Begin(token, view.Revision, CapabilityFan)
			if err != nil {
				t.Fatal(err)
			}
			tx, rx := true, false
			tc.observe(c, &tx, &rx)
			view, err = c.Current(token)
			if err != nil || view.Phase != PhaseFailed || !strings.Contains(view.Failure, "TX observed") {
				t.Fatalf("transient TX was not latched: view=%+v err=%v", view, err)
			}
		})
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

func TestFailReturnsMutatedViewAndRetainsIncompleteReport(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: "home", Kind: ScreenHome})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := c.Fail(token, auth.Revision, "transport stopped")
	if err == nil || failed.Phase != PhaseFailed || failed.Failure != "transport stopped" || failed.Revision <= auth.Revision {
		t.Fatalf("immediate failed view=%+v err=%v", failed, err)
	}
	stored, currentErr := c.Current(token)
	if currentErr != nil || stored.Phase != failed.Phase || stored.Revision != failed.Revision || stored.Failure != failed.Failure {
		t.Fatalf("stored view=%+v err=%v, immediate=%+v", stored, currentErr, failed)
	}
	report := c.Report("EXPERT 1.3K-FA", "unknown", "test")
	if report.Complete || report.Phase != PhaseFailed || len(report.Capabilities) != 1 {
		t.Fatalf("partial report=%+v", report)
	}
	partial := report.Capabilities[0]
	if partial.Complete || partial.IncompletePhase != PhaseDiscovering || partial.Failure != "transport stopped" || len(partial.Actions) != 1 {
		t.Fatalf("partial capability=%+v", partial)
	}
}

func TestAbortRetainsIncompleteNonPromotableReport(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: "home", Kind: ScreenHome})
	if err != nil {
		t.Fatal(err)
	}
	aborted, err := c.Abort(token, auth.Revision, false)
	if err != nil || aborted.Phase != PhaseAborted {
		t.Fatalf("abort view=%+v err=%v", aborted, err)
	}
	report := c.Report("EXPERT 1.3K-FA", "unknown", "test")
	if report.Complete || report.Phase != PhaseAborted || len(report.Capabilities) != 1 || report.Capabilities[0].IncompletePhase != PhaseDiscovering {
		t.Fatalf("aborted report=%+v", report)
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
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: base + 1, Fingerprint: "bank-entry", Candidate: CapabilityBank})
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
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, plan.ExpectedModel, discovery)
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
	return Plan{Profile: profile, ExpectedModel: "EXPERT 1.3K-FA", ExpectedSerialSessionGeneration: 1, Capability: capability, OriginalValue: original, CandidateValue: candidate,
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

func TestReviewedThirdSeriesPlanIsFirmwareBound(t *testing.T) {
	plan := simplePlan(CapabilityFan, "normal", "quiet")
	plan.Profile = "expert-2k-fa-third-series-fan-normal-quiet-v1"
	plan.ExpectedModel = "EXPERT 2K-FA"
	plan.ExpectedFirmware = "Rel.26_03_24_A"
	if err := validatePlan(plan); err != nil {
		t.Fatalf("exact Third Series profile rejected: %v", err)
	}
	plan.ExpectedFirmware = "Rel.26_03_24_B"
	if err := validatePlan(plan); err == nil || !strings.Contains(err.Error(), "exact reviewed firmware") {
		t.Fatalf("wrong Third Series firmware error = %v", err)
	}
	plan.ExpectedFirmware = "rel.26_03_24_a"
	if err := validatePlan(plan); err == nil || !strings.Contains(err.Error(), "exact reviewed firmware") {
		t.Fatalf("case-variant Third Series firmware error = %v", err)
	}
}

func TestReviewedThirdSeriesPlanKeepsCompleteApplyRestoreReceipts(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	c.ObserveStatus(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 2K-FA"}}, 2)
	v, token := arm(t, c)
	plan := simplePlan(CapabilityFan, "normal", "quiet")
	plan.Profile = "expert-2k-fa-third-series-fan-normal-quiet-v1"
	plan.ExpectedModel = "EXPERT 2K-FA"
	plan.ExpectedFirmware = "Rel.26_03_24_A"
	v = completeCapability(t, c, token, v, plan)
	if v.Phase != PhaseArmed || c.session.actionBudget != 48 {
		t.Fatalf("completed Third Series view=%+v budget=%d", v, c.session.actionBudget)
	}
	if len(c.reports) != 1 {
		t.Fatalf("Third Series reports = %d", len(c.reports))
	}
	report := c.reports[0]
	if !report.Complete || !report.AppliedVerified || !report.RestoreVerified || len(report.Verifications) != 2 || !report.Verifications[0].Verified || !report.Verifications[1].Verified {
		t.Fatalf("incomplete Third Series verification receipts: %+v", report)
	}
	if report.OriginalValue != "normal" || report.CandidateValue != "quiet" || len(report.ActionReceipts) == 0 || len(report.Transitions) == 0 {
		t.Fatalf("incomplete Third Series transaction receipts: %+v", report)
	}
}

func TestDiscoveryAuthorizationFreezesObservedAmplifierModel(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}

	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{
		Generation:  c.session.lastEvidenceGen + 1,
		Fingerprint: "home",
		Kind:        ScreenHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth.ExpectedModel != "EXPERT 1.3K-FA" {
		t.Fatalf("discovery authorization model = %q", auth.ExpectedModel)
	}
	if auth.ExpectedSerialSessionGeneration != 1 {
		t.Fatalf("discovery authorization serial session = %d", auth.ExpectedSerialSessionGeneration)
	}
}

func TestDiscoverySessionFailsWhenObservedModelChanges(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 2, 1)
	v, err = c.Current(token)
	if err != nil || v.Phase != PhaseFailed || !strings.Contains(v.Failure, "model changed") {
		t.Fatalf("changed selection model did not fail discovery: view=%+v err=%v", v, err)
	}
}

func TestPlannedSessionFailsWhenObservedModelChanges(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	plan := simplePlan(CapabilityFan, "normal", "contest")
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	base := c.session.lastEvidenceGen
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: base + 1, Fingerprint: "candidate-entry", Candidate: CapabilityFan})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
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
	c.ObserveStatus(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 10)
	v, err = c.Current(token)
	if err != nil || v.Phase != PhaseFailed || !strings.Contains(v.Failure, "model changed") {
		t.Fatalf("changed model did not fail planned session: view=%+v err=%v", v, err)
	}
}

func TestPlannedActionRejectsChangedSerialSessionWithSameModel(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	plan := simplePlan(CapabilityFan, "normal", "contest")
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	base := c.session.lastEvidenceGen
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: base + 1, Fingerprint: "candidate-entry", Candidate: CapabilityFan})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
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
	c.ObserveSerialSession(2)
	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 2, 2)
	v, err = c.Current(token)
	if err != nil {
		t.Fatal(err)
	}
	if v.Phase != PhaseFailed || !strings.Contains(v.Failure, "serial session changed") {
		t.Fatalf("changed session did not immediately fail transaction: %+v", v)
	}
	if _, err = c.AuthorizeNext(token, v.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false)); err == nil {
		t.Fatal("changed-session transaction remained actionable")
	}
}

func TestPendingDiscoveryFailsClosedOnSerialSessionChange(t *testing.T) {
	l := &fakeLease{}
	c, _ := newDeterministicController(l)
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}

	before := display.NewState()
	before.SetRow(0, "BEFORE")
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: Analyze(before).Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	evidenceCount := len(c.session.evidence)

	c.ObserveSerialSession(2)
	if c.session.phase != PhaseFailed || !strings.Contains(c.session.failure, "serial session changed") || l.released != 1 {
		t.Fatalf("reconnect did not fail pending discovery: session=%+v lease=%+v", c.session, l)
	}

	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 2, 2)
	after := display.NewState()
	after.SetRow(0, "AFTER")
	c.ObserveDisplayFromSerialSession(after, 6, true, nil, nil, 2)
	if c.session.phase != PhaseFailed || len(c.session.evidence) != evidenceCount || c.session.revision != auth.Revision+1 {
		t.Fatalf("replacement-session display altered failed discovery: %+v", c.session)
	}
}

func TestPendingPlannedActionFailsClosedOnSerialSessionChange(t *testing.T) {
	l := &fakeLease{}
	c, _ := newDeterministicController(l)
	v, token := arm(t, c)
	plan := simplePlan(CapabilityFan, "NORMAL", "CONTEST")
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	base := c.session.lastEvidenceGen
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: base + 1, Fingerprint: "candidate-entry", Candidate: CapabilityFan})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
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
	auth, err = c.AuthorizeNext(token, v.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
	if err != nil {
		t.Fatal(err)
	}
	evidenceCount := len(c.session.evidence)

	c.ObserveSerialSession(2)
	if c.session.phase != PhaseFailed || !strings.Contains(c.session.failure, "serial session changed") || l.released != 1 {
		t.Fatalf("reconnect did not fail pending planned action: session=%+v lease=%+v", c.session, l)
	}

	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 2, 2)
	replacement := display.NewState()
	replacement.SetRow(0, "REPLACEMENT")
	c.ObserveDisplayFromSerialSession(replacement, base+3, true, nil, nil, 2)
	if c.session.phase != PhaseFailed || len(c.session.evidence) != evidenceCount || c.session.revision != auth.Revision+1 {
		t.Fatalf("replacement-session display altered failed planned action: %+v", c.session)
	}
}

func TestPendingDiscoveryFailsClosedOnSameSessionModelChange(t *testing.T) {
	l := &fakeLease{}
	c, _ := newDeterministicController(l)
	v, token := arm(t, c)
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}

	before := display.NewState()
	before.SetRow(0, "BEFORE")
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: Analyze(before).Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	evidenceCount := len(c.session.evidence)

	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 2, 1)
	if c.session.phase != PhaseFailed || !strings.Contains(c.session.failure, "model changed") || l.released != 1 {
		t.Fatalf("model change did not fail pending discovery: session=%+v lease=%+v", c.session, l)
	}

	after := display.NewState()
	after.SetRow(0, "AFTER")
	c.ObserveDisplayFromSerialSession(after, 6, true, nil, nil, 1)
	if c.session.phase != PhaseFailed || len(c.session.evidence) != evidenceCount || c.session.revision != auth.Revision+1 {
		t.Fatalf("later display altered model-invalidated discovery: %+v", c.session)
	}
}

func TestPendingPlannedActionFailsClosedOnSameSessionModelChange(t *testing.T) {
	l := &fakeLease{}
	c, _ := newDeterministicController(l)
	v, token := arm(t, c)
	plan := simplePlan(CapabilityFan, "NORMAL", "CONTEST")
	v, err := c.Begin(token, v.Revision, CapabilityFan)
	if err != nil {
		t.Fatal(err)
	}
	base := c.session.lastEvidenceGen
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: base + 1, Fingerprint: "candidate-entry", Candidate: CapabilityFan})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
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
	auth, err = c.AuthorizeNext(token, v.Revision, capabilityEvidence(base+2, plan.Apply[0].FromFingerprint, CapabilityFan, plan.OriginalValue, false))
	if err != nil {
		t.Fatal(err)
	}
	evidenceCount := len(c.session.evidence)

	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 2, 1)
	if c.session.phase != PhaseFailed || !strings.Contains(c.session.failure, "model changed") || l.released != 1 {
		t.Fatalf("model change did not fail pending planned action: session=%+v lease=%+v", c.session, l)
	}

	replacement := display.NewState()
	replacement.SetRow(0, "REPLACEMENT")
	c.ObserveDisplayFromSerialSession(replacement, base+3, true, nil, nil, 1)
	if c.session.phase != PhaseFailed || len(c.session.evidence) != evidenceCount || c.session.revision != auth.Revision+1 {
		t.Fatalf("later display altered model-invalidated planned action: %+v", c.session)
	}
}

func TestSerialSessionChangeInvalidatesCompletedCapabilityEvidence(t *testing.T) {
	l := &fakeLease{}
	c, _ := newDeterministicController(l)
	v, token := arm(t, c)
	v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
	if v.Phase != PhaseArmed || len(c.reports) != 1 || l.acquired != 1 || l.released != 1 {
		t.Fatalf("completed capability state: view=%+v reports=%+v lease=%+v", v, c.reports, l)
	}
	previousRevision := v.Revision

	c.ObserveSerialSession(2)

	v, err := c.Current(token)
	if err != nil {
		t.Fatal(err)
	}
	if v.Phase != PhaseFailed || !strings.Contains(v.Failure, "serial session changed") || v.Revision != previousRevision+1 {
		t.Fatalf("reconnect did not invalidate active session: %+v", v)
	}
	if len(c.Report("EXPERT 9K-FA", "replacement", "test").Capabilities) != 0 {
		t.Fatalf("replacement model received prior capability evidence: %+v", c.reports)
	}
	if l.released != 1 {
		t.Fatalf("reconnect released an already released lease: %+v", l)
	}
	if _, err = c.Begin(token, v.Revision, CapabilityBank); err == nil {
		t.Fatal("invalidated session continued with another capability")
	}
}

func TestSerialSessionChangeInvalidatesCompletedSessionReport(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
	v, err := c.Complete(token, v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if v.Phase != PhaseComplete || len(c.reports) != 1 {
		t.Fatalf("completed session state: view=%+v reports=%+v", v, c.reports)
	}
	previousRevision := v.Revision

	c.ObserveSerialSession(2)

	v, err = c.Current(token)
	if err != nil {
		t.Fatal(err)
	}
	if v.Phase != PhaseFailed || v.Revision != previousRevision+1 || len(c.reports) != 0 {
		t.Fatalf("completed report survived reconnect: view=%+v reports=%+v", v, c.reports)
	}
}

func TestSerialSessionChangeInvalidatesReportsRetainedByTerminalSessions(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*testing.T, *Controller, *time.Time, string, SessionView) SessionView
	}{
		{
			name: "failed",
			terminate: func(t *testing.T, c *Controller, _ *time.Time, token string, v SessionView) SessionView {
				t.Helper()
				if _, err := c.Fail(token, v.Revision, "test failure"); err == nil {
					t.Fatal("Fail returned no error")
				}
				view, err := c.Current(token)
				if err != nil || view.Phase != PhaseFailed {
					t.Fatalf("Current after Fail: view=%+v err=%v", view, err)
				}
				return view
			},
		},
		{
			name: "aborted",
			terminate: func(t *testing.T, c *Controller, _ *time.Time, token string, v SessionView) SessionView {
				t.Helper()
				view, err := c.Abort(token, v.Revision, true)
				if err != nil || view.Phase != PhaseAborted {
					t.Fatalf("Abort: view=%+v err=%v", view, err)
				}
				return view
			},
		},
		{
			name: "expired",
			terminate: func(t *testing.T, c *Controller, now *time.Time, _ string, _ SessionView) SessionView {
				t.Helper()
				*now = now.Add(c.lifetime)
				view := c.Tick(*now)
				if view.Phase != PhaseExpired {
					t.Fatalf("Tick: view=%+v", view)
				}
				return view
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, now := newDeterministicController(&fakeLease{})
			v, token := arm(t, c)
			v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
			v = tc.terminate(t, c, now, token, v)
			if len(c.reports) != 1 {
				t.Fatalf("terminal session did not retain setup report: %+v", c.reports)
			}
			previousRevision := v.Revision

			c.ObserveSerialSession(2)

			if c.session.phase != PhaseFailed || !strings.Contains(c.session.failure, "serial session changed") || c.session.revision != previousRevision+1 || len(c.reports) != 0 {
				t.Fatalf("terminal report survived reconnect: session=%+v reports=%+v", c.session, c.reports)
			}
		})
	}
}

func TestSerialSessionChangePreservesIdleControllerWithoutReports(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	c.ObserveSerialSession(2)
	if c.session.phase != PhaseIdle || c.session.revision != 0 || len(c.reports) != 0 {
		t.Fatalf("idle controller changed on reconnect: session=%+v reports=%+v", c.session, c.reports)
	}
}

func TestSameSessionModelChangeInvalidatesReportsInEveryPhase(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*testing.T, *Controller, *time.Time, string, SessionView) SessionView
	}{
		{
			name: "complete",
			terminate: func(t *testing.T, c *Controller, _ *time.Time, token string, v SessionView) SessionView {
				t.Helper()
				view, err := c.Complete(token, v.Revision)
				if err != nil || view.Phase != PhaseComplete {
					t.Fatalf("Complete: view=%+v err=%v", view, err)
				}
				return view
			},
		},
		{
			name: "failed",
			terminate: func(t *testing.T, c *Controller, _ *time.Time, token string, v SessionView) SessionView {
				t.Helper()
				if _, err := c.Fail(token, v.Revision, "test failure"); err == nil {
					t.Fatal("Fail returned no error")
				}
				view, err := c.Current(token)
				if err != nil || view.Phase != PhaseFailed {
					t.Fatalf("Current after Fail: view=%+v err=%v", view, err)
				}
				return view
			},
		},
		{
			name: "aborted",
			terminate: func(t *testing.T, c *Controller, _ *time.Time, token string, v SessionView) SessionView {
				t.Helper()
				view, err := c.Abort(token, v.Revision, true)
				if err != nil || view.Phase != PhaseAborted {
					t.Fatalf("Abort: view=%+v err=%v", view, err)
				}
				return view
			},
		},
		{
			name: "expired",
			terminate: func(t *testing.T, c *Controller, now *time.Time, _ string, _ SessionView) SessionView {
				t.Helper()
				*now = now.Add(c.lifetime)
				view := c.Tick(*now)
				if view.Phase != PhaseExpired {
					t.Fatalf("Tick: view=%+v", view)
				}
				return view
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, now := newDeterministicController(&fakeLease{})
			v, token := arm(t, c)
			v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
			v = tc.terminate(t, c, now, token, v)
			if len(c.reports) != 1 {
				t.Fatalf("terminal session did not retain report: %+v", c.reports)
			}
			previousRevision := v.Revision

			c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 2, 1)

			if c.session.phase != PhaseFailed || !strings.Contains(c.session.failure, "model changed") || c.session.revision != previousRevision+1 || len(c.reports) != 0 {
				t.Fatalf("report survived model change: session=%+v reports=%+v", c.session, c.reports)
			}
			if got := c.Report("EXPERT 1.5K-FA", "replacement", "test"); len(got.Capabilities) != 0 {
				t.Fatalf("replacement model received prior report evidence: %+v", got)
			}
		})
	}
}

func TestModelObservationPreservesControllersWithoutBoundEvidence(t *testing.T) {
	t.Run("initial empty to model", func(t *testing.T) {
		c := NewController(&fakeLease{})
		c.now = func() time.Time { return time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC) }
		c.newToken = func() (string, error) { return "test-token", nil }
		c.ObserveSerialSession(1)
		v, _, err := c.Arm(Acknowledgement, readyPrerequisites())
		if err != nil {
			t.Fatal(err)
		}
		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 1, 1)
		if c.session.phase != PhaseArmed || c.session.revision != v.Revision || c.runtime.Status.ModelName != "EXPERT 1.3K-FA" {
			t.Fatalf("initial model observation invalidated active controller: session=%+v runtime=%+v", c.session, c.runtime)
		}
	})

	t.Run("idle model change", func(t *testing.T) {
		c, _ := newDeterministicController(&fakeLease{})
		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 2, 1)
		if c.session.phase != PhaseIdle || c.session.revision != 0 || c.runtime.Status.ModelName != "EXPERT 1.5K-FA" {
			t.Fatalf("idle model change invalidated controller: session=%+v runtime=%+v", c.session, c.runtime)
		}
	})
}

func TestEmptyModelObservationPreservesActiveSessionAndRetainedReport(t *testing.T) {
	t.Run("pending receipt completion attribution", func(t *testing.T) {
		c, _ := newDeterministicController(&fakeLease{})
		v, token := arm(t, c)
		v, err := c.Begin(token, v.Revision, CapabilityBank)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: "home", Kind: ScreenHome})
		if err != nil {
			t.Fatal(err)
		}

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{}}, 2, 1)
		if got := c.Runtime().Status.ModelName; got != "EXPERT 1.3K-FA" {
			t.Fatalf("pending receipt model = %q, want retained model", got)
		}
		v, err = c.ObserveDiscoveryResult(token, auth.Revision, Evidence{Generation: 6, Fingerprint: "bank", Kind: ScreenBank, Candidate: CapabilityBank, Value: "A"})
		if err != nil {
			t.Fatal(err)
		}
		v, err = c.CompleteTopology(token, v.Revision)
		if err != nil {
			t.Fatal(err)
		}
		report := c.Report(c.Runtime().Status.ModelName, "unknown", "test")
		if report.Model != "EXPERT 1.3K-FA" || len(report.Capabilities) != 1 {
			t.Fatalf("completed receipt report attribution = %+v", report)
		}
	})

	t.Run("active session", func(t *testing.T) {
		l := &fakeLease{}
		c, _ := newDeterministicController(l)
		v, token := arm(t, c)
		v, err := c.Begin(token, v.Revision, CapabilityFan)
		if err != nil {
			t.Fatal(err)
		}
		previousRevision := v.Revision

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{}}, 2, 1)

		v, err = c.Current(token)
		if err != nil || v.Phase != PhaseDiscovering || v.Revision != previousRevision || l.released != 0 || c.Runtime().Status.ModelName != "EXPERT 1.3K-FA" {
			t.Fatalf("empty model invalidated active session: view=%+v err=%v lease=%+v", v, err, l)
		}

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 3, 1)
		v, err = c.Current(token)
		if err != nil || v.Phase != PhaseDiscovering || v.Revision != previousRevision || l.released != 0 {
			t.Fatalf("same model after empty status invalidated active session: view=%+v err=%v lease=%+v", v, err, l)
		}

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 4, 1)
		v, err = c.Current(token)
		if err != nil || v.Phase != PhaseFailed || !strings.Contains(v.Failure, "model changed") || v.Revision != previousRevision+1 || l.released != 1 {
			t.Fatalf("different model after empty status did not invalidate active session: view=%+v err=%v lease=%+v", v, err, l)
		}
	})

	t.Run("retained report", func(t *testing.T) {
		c, _ := newDeterministicController(&fakeLease{})
		v, token := arm(t, c)
		v = completeCapability(t, c, token, v, simplePlan(CapabilityFan, "NORMAL", "CONTEST"))
		v, err := c.Complete(token, v.Revision)
		if err != nil {
			t.Fatal(err)
		}
		previousRevision := v.Revision

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{}}, 2, 1)

		v, err = c.Current(token)
		if err != nil || v.Phase != PhaseComplete || v.Revision != previousRevision || len(c.reports) != 1 || c.Runtime().Status.ModelName != "EXPERT 1.3K-FA" {
			t.Fatalf("empty model invalidated retained report: view=%+v err=%v reports=%+v", v, err, c.reports)
		}

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.3K-FA"}}, 3, 1)
		v, err = c.Current(token)
		if err != nil || v.Phase != PhaseComplete || v.Revision != previousRevision || len(c.reports) != 1 {
			t.Fatalf("same model after empty status invalidated retained report: view=%+v err=%v reports=%+v", v, err, c.reports)
		}

		c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 4, 1)
		v, err = c.Current(token)
		if err != nil || v.Phase != PhaseFailed || !strings.Contains(v.Failure, "model changed") || v.Revision != previousRevision+1 || len(c.reports) != 0 {
			t.Fatalf("different model after empty status did not clear retained report: view=%+v err=%v reports=%+v", v, err, c.reports)
		}
	})
}

func TestReviewedPlanDiscoveryTopologyMustMatchExactly(t *testing.T) {
	evidence := []Evidence{
		{Kind: ScreenHome},
		{Kind: ScreenSetup, Selection: "CONFIG"},
		{Kind: ScreenSetup, Selection: "ANTENNA"},
		{Kind: ScreenSetup, Selection: "BEEP    Off"},
		{Kind: ScreenSetup, Selection: "RX  ANT"},
		{Kind: ScreenFan, Selection: "FAN MANAGEMENT"},
	}
	expected := []string{"CONFIG", "ANTENNA", "~BEEP", "RX ANT"}
	if !matchesDiscoverySetupWaypoints(evidence, "", expected) {
		t.Fatal("captured setup topology with LCD field padding did not match")
	}
	for _, nearMatch := range [][]string{
		{"ANTENNA", "CONFIG", "~BEEP", "RX ANT"},
		{"CONFIG", "ANTENNA", "~BEEP"},
		{"CONFIG", "ANTENNA", "~START", "RX ANT"},
		{"CONFIG", "ANTENNA", "~BEEP", "RX AT"},
	} {
		if matchesDiscoverySetupWaypoints(evidence, "", nearMatch) {
			t.Fatalf("near-match topology was accepted: %v", nearMatch)
		}
	}
}

func TestStepExpectationAcceptsLCDFieldPaddingOnly(t *testing.T) {
	step := Step{ExpectedKind: ScreenSetup, ExpectedSelection: "RX ANT", ExpectedSetupTopology: SetupTopologyThirdSeries2K}
	evidence := Evidence{Kind: ScreenSetup, Selection: "RX  ANT", SetupTopology: SetupTopologyThirdSeries2K}
	if err := matchesStepExpectation(step, evidence); err != nil {
		t.Fatalf("field-padded Third Series waypoint rejected: %v", err)
	}

	evidence.Selection = "RX AT"
	if err := matchesStepExpectation(step, evidence); err == nil {
		t.Fatal("different menu label was accepted after whitespace normalization")
	}
	evidence.Selection = "RX  ANT"
	evidence.SetupTopology = SetupTopologySecondSeries
	if err := matchesStepExpectation(step, evidence); err == nil {
		t.Fatal("cross-family waypoint was accepted after whitespace normalization")
	}

	evidence.SetupTopology = SetupTopologyThirdSeries2K
	for _, selection := range []string{"RX\nANT", "RX\rANT", "RX\tANT", "RX\u00a0ANT", "\nRX  ANT", "RX  ANT\n", "\u00a0RX  ANT", "RX  ANT\u00a0"} {
		evidence.Selection = selection
		if err := matchesStepExpectation(step, evidence); err == nil {
			t.Fatalf("separate or non-ASCII-padded selection %q was accepted", selection)
		}
	}
}

func TestReviewedThirdSeriesDiscoveryWaypointsMatchSanitizedReport(t *testing.T) {
	selections := []string{
		"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP    On",
		"START   Oprt", "TEMP.    F", "ALARMS LOG", "TUN ANT", "RX  ANT", "FAN NOISE",
	}
	evidence := make([]Evidence, 0, len(selections)+2)
	evidence = append(evidence, Evidence{Kind: ScreenHome})
	for _, selection := range selections {
		evidence = append(evidence, Evidence{Kind: ScreenSetup, Selection: selection, SetupTopology: SetupTopologyThirdSeries2K})
	}
	evidence = append(evidence, Evidence{Kind: ScreenFan, Selection: "[ ] NORMAL MODE (ALL MODES)"})
	expected := []string{
		"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "~BEEP",
		"~START", "~TEMP.", "ALARMS LOG", "TUN ANT", "RX ANT", "FAN NOISE",
	}
	if !matchesDiscoverySetupWaypoints(evidence, SetupTopologyThirdSeries2K, expected) {
		t.Fatal("sanitized Third Series report waypoints did not match the reviewed topology")
	}

	for _, selection := range []string{"RX\nANT", "\nRX  ANT", "RX  ANT\n", "\u00a0RX  ANT", "RX  ANT\u00a0"} {
		evidence[11].Selection = selection
		if matchesDiscoverySetupWaypoints(evidence, SetupTopologyThirdSeries2K, expected) {
			t.Fatalf("invalid exact waypoint selection %q was accepted", selection)
		}
	}
	evidence[11].Selection = "RX  ANT"
	for _, invalid := range []struct {
		index     int
		selection string
	}{
		{index: 6, selection: "BEEP\nOn"},
		{index: 6, selection: "\nBEEP    On"},
		{index: 6, selection: "BEEP    On\n"},
		{index: 6, selection: "\u00a0BEEP    On"},
		{index: 6, selection: "BEEP    On\u00a0"},
		{index: 7, selection: "START\u00a0Oprt"},
		{index: 8, selection: "TEMP.\tF"},
	} {
		original := evidence[invalid.index].Selection
		evidence[invalid.index].Selection = invalid.selection
		if matchesDiscoverySetupWaypoints(evidence, SetupTopologyThirdSeries2K, expected) {
			t.Fatalf("invalid dynamic waypoint selection %q was accepted", invalid.selection)
		}
		evidence[invalid.index].Selection = original
	}
	if !matchesDiscoverySetupWaypoints(evidence, SetupTopologyThirdSeries2K, expected) {
		t.Fatal("ASCII-padded dynamic waypoint suffixes were rejected")
	}

	evidence[11].SetupTopology = SetupTopologySecondSeries
	if matchesDiscoverySetupWaypoints(evidence, SetupTopologyThirdSeries2K, expected) {
		t.Fatal("cross-family waypoint in sanitized report sequence was accepted")
	}
}

func TestReviewedPlanDiscoveryTopologyRejectsCrossFamilySelections(t *testing.T) {
	evidence := []Evidence{
		{Kind: ScreenSetup, Selection: "ANTENNA", SetupTopology: SetupTopologySecondSeries},
		{Kind: ScreenSetup, Selection: "CAT", SetupTopology: SetupTopologySecondSeries},
	}
	if matchesDiscoverySetupWaypoints(evidence, SetupTopologyFirstSeries, []string{"ANTENNA", "CAT"}) {
		t.Fatal("matching labels from the wrong setup family were accepted")
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
		auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: "fan-entry", Candidate: CapabilityFan})
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
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", evidence(5, "screen-a"))
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, evidence(6, "screen-b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", evidence(6, "screen-b")); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget error=%v", err)
	}

	c, _ = newDeterministicController(&fakeLease{})
	v, token = arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityBank)
	if _, err = c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: "fan", Candidate: CapabilityFan}); err == nil || !strings.Contains(err.Error(), "server-identified") {
		t.Fatalf("candidate error=%v", err)
	}
}

func TestDiscoveryLoopFailsClosed(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityFan)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", evidence(5, "same-highlight-aware-fingerprint"))
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
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: beforeScreen.Fingerprint})
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
	after.Attrs[0][0] = 0x80
	c.ObserveDisplay(after, 7, true, nil, nil)
	if c.session.phase != PhaseDiscovering || c.session.discoveryPending {
		t.Fatalf("distinct screen did not complete discovery action: %+v", c.session)
	}
	if len(c.session.evidence) == 0 {
		t.Fatal("distinct observed screen did not produce evidence")
	}
	observed := c.session.evidence[len(c.session.evidence)-1]
	if observed.RawState == nil || observed.RawState.Chars != after.Chars || observed.RawState.Attrs != after.Attrs {
		t.Fatalf("observed evidence did not preserve exact raw display state: %+v", observed.RawState)
	}
}

func TestTopologyDisplayExitCompletesOnlyAtVerifiedHomeWithoutSave(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityBank)
	home := display.NewState()
	home.SetRow(6, "IN  BAND ANT BNK  CAT   OUT   SWR   TEMP")
	homeFingerprint := Analyze(home).Fingerprint
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: homeFingerprint, Kind: ScreenHome})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, Evidence{Generation: 6, Fingerprint: "bank", Kind: ScreenBank, Candidate: CapabilityBank, Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err = c.AuthorizeTopologyExit(token, v.Revision, "EXPERT 1.3K-FA", Evidence{Generation: 6, Fingerprint: "bank", Kind: ScreenBank, Candidate: CapabilityBank, Value: "A"})
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

func TestTopologySessionFailsWhenObservedModelChanges(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityBank)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionSet, "EXPERT 1.3K-FA", Evidence{Generation: 5, Fingerprint: "home", Kind: ScreenHome})
	if err != nil {
		t.Fatal(err)
	}
	v, err = c.ObserveDiscoveryResult(token, auth.Revision, Evidence{Generation: 6, Fingerprint: "bank", Kind: ScreenBank, Candidate: CapabilityBank, Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	c.ObserveStatusFromSerialSession(api.Status{Telemetry: api.Telemetry{ModelName: "EXPERT 1.5K-FA"}}, 2, 1)
	v, err = c.Current(token)
	if err != nil || v.Phase != PhaseFailed || !strings.Contains(v.Failure, "model changed") {
		t.Fatalf("changed model did not fail topology session: view=%+v err=%v", v, err)
	}
}

func TestDisplayEvidenceMustAdvance(t *testing.T) {
	c, _ := newDeterministicController(&fakeLease{})
	v, token := arm(t, c)
	v, _ = c.Begin(token, v.Revision, CapabilityFan)
	auth, err := c.AuthorizeDiscovery(token, v.Revision, ActionRight, "EXPERT 1.3K-FA", evidence(5, "screen-a"))
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
	c.reports[0].Evidence[0].SetupTopology = SetupTopologyFirstSeries
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
	if !strings.Contains(jsonText, `"setupTopology":"expert-first-series-setup-v1"`) {
		t.Fatalf("missing setup topology: %s", jsonText)
	}
}
