package menudebug

import (
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

const ReportSchemaVersion = "menu-report.v1"

type Capability string

const (
	CapabilityFan  Capability = "fan"
	CapabilityBank Capability = "bank"
)

type Phase string

const (
	PhaseIdle                  Phase = "idle"
	PhaseArmed                 Phase = "armed"
	PhaseDiscovering           Phase = "discovering"
	PhasePlanReady             Phase = "plan-ready"
	PhaseApplying              Phase = "applying"
	PhaseAwaitingApplyVerify   Phase = "awaiting-apply-verification"
	PhaseRestoring             Phase = "restoring"
	PhaseAwaitingRestoreVerify Phase = "awaiting-restore-verification"
	PhaseComplete              Phase = "complete"
	PhaseFailed                Phase = "failed"
	PhaseExpired               Phase = "expired"
	PhaseAborted               Phase = "aborted"
)

type Action string

const (
	ActionRight   Action = "right"
	ActionSet     Action = "set"
	ActionDisplay Action = "display"
)

type Purpose string

const (
	PurposeEnumerate       Purpose = "enumerate"
	PurposeEnterCandidate  Purpose = "enter-candidate"
	PurposeChangeValue     Purpose = "change-value"
	PurposeSave            Purpose = "save"
	PurposeExitWithoutSave Purpose = "exit-without-save"
)

type Prerequisites struct {
	DebugEnabled             bool
	RecentProtocolStatus     bool
	ProtocolStandby          bool
	ProtocolRX               bool
	ChecksumValidDisplay     bool
	DisplayStandby           bool
	DisplayRX                bool
	HomeDisplay              bool
	AutomaticFanPolicyActive bool
	ManualFanControlActive   bool
	OvertemperatureArmed     bool
	DisplayGeneration        uint64
	StatusGeneration         uint64
}

// RuntimeSnapshot is the latest server-owned protocol and display evidence.
// It deliberately contains no host, network, or serial-device identity.
type RuntimeSnapshot struct {
	Status            api.Status
	StatusGeneration  uint64
	StatusObservedAt  time.Time
	DisplayState      display.State
	DisplayGeneration uint64
	DisplayObservedAt time.Time
	ChecksumValid     bool
	DisplayTX         *bool
	DisplayOperate    *bool
	Screen            ScreenObservation
}

type Evidence struct {
	Generation  uint64     `json:"generation"`
	Fingerprint string     `json:"fingerprint"`
	Kind        ScreenKind `json:"kind,omitempty"`
	Rows        [8]string  `json:"rows"`
	Selection   string     `json:"selection,omitempty"`
	Candidate   Capability `json:"candidate,omitempty"`
	Value       string     `json:"value,omitempty"`
	SaveVisible bool       `json:"saveVisible,omitempty"`
	StandbyHome bool       `json:"standbyHome,omitempty"`
	ObservedAt  time.Time  `json:"observedAt"`
}

type Step struct {
	Action                    Action     `json:"action"`
	Purpose                   Purpose    `json:"purpose"`
	FromFingerprint           string     `json:"fromFingerprint,omitempty"`
	ExpectedFingerprint       string     `json:"expectedFingerprint,omitempty"`
	ExpectedKind              ScreenKind `json:"expectedKind,omitempty"`
	ExpectedCapability        Capability `json:"expectedCapability,omitempty"`
	ExpectedValue             string     `json:"expectedValue,omitempty"`
	ExpectedSelection         string     `json:"expectedSelection,omitempty"`
	ExpectedSelectionContains string     `json:"expectedSelectionContains,omitempty"`
	ExpectedSaveVisible       bool       `json:"expectedSaveVisible,omitempty"`
	ExpectedStandbyHome       bool       `json:"expectedStandbyHome,omitempty"`
	AllowStoringBeforeHome    bool       `json:"allowStoringBeforeHome,omitempty"`
}

type Plan struct {
	Profile                 string     `json:"profile"`
	Capability              Capability `json:"capability"`
	OriginalValue           string     `json:"originalValue"`
	CandidateValue          string     `json:"candidateValue"`
	DiscoverySetupWaypoints []string   `json:"-"`
	Apply                   []Step     `json:"apply"`
	Restore                 []Step     `json:"restore"`
}

type ActionAuthorization struct {
	Action   Action
	Purpose  Purpose
	Revision uint64
}

type SessionView struct {
	ID                   string            `json:"id"`
	Phase                Phase             `json:"phase"`
	Capability           Capability        `json:"capability,omitempty"`
	Revision             uint64            `json:"revision"`
	ExpiresAt            string            `json:"expiresAt,omitempty"`
	ActionsAttempted     int               `json:"actionsAttempted"`
	ActionBudget         int               `json:"actionBudget"`
	MayBeInMenu          bool              `json:"mayBeInMenu"`
	Failure              string            `json:"failure,omitempty"`
	Completed            []Capability      `json:"completedCapabilities"`
	RecoveryInstructions string            `json:"recoveryInstructions,omitempty"`
	PlanProfile          string            `json:"planProfile,omitempty"`
	OriginalValue        string            `json:"originalValue,omitempty"`
	CandidateValue       string            `json:"candidateValue,omitempty"`
	StepNumber           int               `json:"stepNumber,omitempty"`
	TotalSteps           int               `json:"totalSteps,omitempty"`
	PlanSummary          []PlanStepSummary `json:"planSummary,omitempty"`
	LastVerifiedScreen   string            `json:"lastVerifiedScreen,omitempty"`
}

type PlanStepSummary struct {
	Transaction string  `json:"transaction"`
	Number      int     `json:"number"`
	Action      Action  `json:"action"`
	Purpose     Purpose `json:"purpose"`
	Expected    string  `json:"expected"`
}

// Report is deliberately closed: it has no arbitrary metadata map and no
// fields for addresses, hostnames, callsigns, tokens, or serial device paths.
type Report struct {
	SchemaVersion string             `json:"schemaVersion"`
	Model         string             `json:"model"`
	Firmware      string             `json:"firmware"`
	ServerVersion string             `json:"serverVersion"`
	Capabilities  []CapabilityReport `json:"capabilities"`
}

type CapabilityReport struct {
	Profile         string                `json:"profile"`
	Capability      Capability            `json:"capability"`
	OriginalValue   string                `json:"originalValue"`
	CandidateValue  string                `json:"candidateValue"`
	AppliedVerified bool                  `json:"appliedVerified"`
	RestoreVerified bool                  `json:"restoreVerified"`
	Evidence        []Evidence            `json:"evidence"`
	Actions         []Action              `json:"actions"`
	ActionReceipts  []ActionReceipt       `json:"actionReceipts"`
	Transitions     []TransitionReceipt   `json:"transitions"`
	Verifications   []VerificationReceipt `json:"verifications"`
}

type ActionReceipt struct {
	Action  Action    `json:"action"`
	Purpose Purpose   `json:"purpose"`
	At      time.Time `json:"at"`
}

type TransitionReceipt struct {
	FromFingerprint string     `json:"fromFingerprint,omitempty"`
	ToFingerprint   string     `json:"toFingerprint"`
	Kind            ScreenKind `json:"kind"`
	At              time.Time  `json:"at"`
}

type VerificationReceipt struct {
	Phase    Phase     `json:"phase"`
	Verified bool      `json:"verified"`
	At       time.Time `json:"at"`
}
