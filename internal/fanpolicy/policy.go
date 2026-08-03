package fanpolicy

import (
	"math"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

const (
	PolicyUnknown = "unknown"
	PolicyNormal  = "normal"
	PolicyHigh    = "high-cooling"

	FirstSeriesDisplayProfile  = "expert-1.3k-fa-first-series-v1"
	SecondSeriesDisplayProfile = "expert-1.5k-fa-second-series-v1"
	SupportedDisplayProfile    = FirstSeriesDisplayProfile

	StateDisabled    = "disabled"
	StateUnavailable = "unavailable"
	StateNormal      = "normal"
	StateBlocked     = "blocked"
	StatePending     = "pending"
	StateNavigating  = "navigating"
	StatePaused      = "paused"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
)

type Settings struct {
	Enabled            bool
	HighTemperatureC   float64
	NormalTemperatureC float64
	DisplayProfile     string
	SafetyStandbyArmed bool
	SafetyStandbyTripC float64
	OverridePolicy     string
	VerifyRequested    bool
}

type Thresholds struct {
	HighTemperatureC   float64 `json:"highTemperatureC"`
	NormalTemperatureC float64 `json:"normalTemperatureC"`
}

type Observations struct {
	MaximumTemperatureC *float64 `json:"maximumTemperatureC,omitempty"`
	TemperatureSensor   string   `json:"temperatureSensor,omitempty"`
	OperatingState      string   `json:"operatingState,omitempty"`
	TX                  *bool    `json:"tx,omitempty"`
	RecentContact       bool     `json:"recentContact"`
	Provenance          string   `json:"provenance,omitempty"`
	ModelName           string   `json:"modelName,omitempty"`
}

type Navigation struct {
	State                 string   `json:"state"`
	Profile               string   `json:"profile,omitempty"`
	LastAction            string   `json:"lastAction,omitempty"`
	LastError             string   `json:"lastError,omitempty"`
	LastVerifiedScreen    string   `json:"lastVerifiedScreen,omitempty"`
	ActionsTaken          []string `json:"actionsTaken"`
	Paused                bool     `json:"paused"`
	PauseReason           string   `json:"pauseReason,omitempty"`
	MayBeInMenu           bool     `json:"mayBeInMenu"`
	ChangedOperatingState bool     `json:"changedOperatingState"`
	RestoreOperatePending bool     `json:"restoreOperatePending"`
	RecoveryState         string   `json:"recoveryState"`
	RecoveryInstructions  string   `json:"recoveryInstructions,omitempty"`
}

type Result struct {
	Enabled                 bool           `json:"enabled"`
	ControlActive           bool           `json:"controlActive"`
	State                   string         `json:"state"`
	Reason                  string         `json:"reason,omitempty"`
	BlockedBy               []string       `json:"blockedBy"`
	Thresholds              Thresholds     `json:"thresholds"`
	Observations            Observations   `json:"observations"`
	DesiredPolicy           string         `json:"desiredPolicy"`
	DesiredPolicySource     string         `json:"desiredPolicySource,omitempty"`
	CurrentPolicy           string         `json:"currentPolicy"`
	CurrentPolicyVerifiedAt string         `json:"currentPolicyVerifiedAt,omitempty"`
	CurrentPolicyConfidence string         `json:"currentPolicyConfidence"`
	CurrentPolicySource     string         `json:"currentPolicySource,omitempty"`
	ManualOverride          ManualOverride `json:"manualOverride"`
	Verification            Verification   `json:"verification"`
	PersistenceError        string         `json:"persistenceError,omitempty"`
	CooldownUntil           string         `json:"cooldownUntil,omitempty"`
	Pending                 bool           `json:"pending"`
	ActionAvailable         bool           `json:"actionAvailable"`
	SupportedModes          []string       `json:"supportedModes"`
	Navigation              Navigation     `json:"navigation"`
}

type ManualOverride struct {
	Active          bool   `json:"active"`
	Policy          string `json:"policy,omitempty"`
	DurationMinutes int    `json:"durationMinutes,omitempty"`
	Until           string `json:"until,omitempty"`
}

type Verification struct {
	Requested bool   `json:"requested"`
	Reason    string `json:"reason,omitempty"`
}

func Evaluate(status api.Status, settings Settings, previousDesired string) Result {
	overridePolicy := normalizePolicy(settings.OverridePolicy)
	controlActive := settings.Enabled || overridePolicy != PolicyUnknown || settings.VerifyRequested
	result := Result{
		Enabled:       settings.Enabled,
		ControlActive: controlActive,
		State:         StateDisabled,
		Reason:        "automatic fan-policy switching and manual override are disabled",
		Thresholds: Thresholds{
			HighTemperatureC:   settings.HighTemperatureC,
			NormalTemperatureC: settings.NormalTemperatureC,
		},
		Observations:            observations(status),
		DesiredPolicy:           normalizePolicy(previousDesired),
		CurrentPolicy:           PolicyUnknown,
		CurrentPolicyConfidence: "unknown",
		BlockedBy:               []string{},
		SupportedModes:          supportedFanModesForModel(status.ModelName),
		Navigation:              Navigation{State: "idle", ActionsTaken: []string{}},
		ManualOverride:          ManualOverride{Active: overridePolicy != PolicyUnknown},
		Verification:            Verification{Requested: settings.VerifyRequested},
	}
	if result.ManualOverride.Active {
		result.ManualOverride.Policy = overridePolicy
	}
	if !controlActive {
		return result
	}
	if overridePolicy != PolicyUnknown {
		result.DesiredPolicy = overridePolicy
		result.DesiredPolicySource = "manual-override"
	} else if settings.VerifyRequested {
		result.DesiredPolicy = PolicyUnknown
		result.DesiredPolicySource = "verification"
	}
	if status.Provenance != "status-poll" || !status.RecentContact {
		result.State = StateUnavailable
		result.Reason = "recent protocol-native temperature status is unavailable"
		result.DesiredPolicy = PolicyUnknown
		return result
	}
	if overridePolicy == PolicyUnknown && !settings.VerifyRequested && result.Observations.MaximumTemperatureC == nil {
		result.State = StateUnavailable
		result.Reason = "no temperature reading is available"
		result.DesiredPolicy = PolicyUnknown
		return result
	}

	if overridePolicy == PolicyUnknown && !settings.VerifyRequested {
		result.DesiredPolicySource = "temperature"
		temperature := *result.Observations.MaximumTemperatureC
		switch {
		case temperature >= settings.HighTemperatureC:
			result.DesiredPolicy = PolicyHigh
		case temperature <= settings.NormalTemperatureC:
			result.DesiredPolicy = PolicyNormal
		case result.DesiredPolicy == PolicyUnknown:
			result.State = StateNormal
			result.Reason = "temperature is inside the hysteresis band and no prior desired policy is known"
			return result
		}
	}

	result.BlockedBy = actionBlocks(status, settings)
	if len(result.BlockedBy) != 0 {
		result.State = StateBlocked
		switch {
		case containsBlock(result.BlockedBy, "rx"):
			result.Reason = "fan-policy change is waiting for RX; no menu writes are sent during TX"
		case containsBlock(result.BlockedBy, "known-rx"):
			result.Reason = "fan-policy change is waiting for an explicit RX state"
		default:
			result.Reason = "fan-policy change is blocked by safety preconditions"
		}
		return result
	}
	result.Pending = true
	result.State = StatePending
	if settings.VerifyRequested {
		result.Reason = "fan-mode verification is waiting for a verified home display"
	} else {
		result.Reason = "fan-policy change is waiting for a verified home display"
	}
	return result
}

func observations(status api.Status) Observations {
	temperature, sensor := maximumTemperature(
		namedReading{"upper", status.TemperatureC},
		namedReading{"lower", status.TemperatureLowerC},
		namedReading{"combiner", status.TemperatureCombinerC},
	)
	return Observations{
		MaximumTemperatureC: temperature,
		TemperatureSensor:   sensor,
		OperatingState:      strings.ToLower(strings.TrimSpace(status.OperatingState)),
		TX:                  status.TX,
		RecentContact:       status.RecentContact,
		Provenance:          status.Provenance,
		ModelName:           strings.TrimSpace(status.ModelName),
	}
}

func actionBlocks(status api.Status, settings Settings) []string {
	var blocked []string
	if status.Provenance != "status-poll" || !status.RecentContact {
		blocked = append(blocked, "fresh-status")
	}
	if status.TX == nil {
		blocked = append(blocked, "known-rx")
	} else if *status.TX {
		blocked = append(blocked, "rx")
	}
	operatingState := strings.ToLower(strings.TrimSpace(status.OperatingState))
	if operatingState != "standby" && operatingState != "operate" {
		blocked = append(blocked, "operating-state")
	}
	if _, ok := verifiedFanDisplayProfileForModel(status.ModelName); !ok {
		blocked = append(blocked, "supported-fan-profile")
	}
	if settings.SafetyStandbyArmed && settings.SafetyStandbyTripC > 0 {
		if temperature, _ := maximumTemperature(
			namedReading{"upper", status.TemperatureC},
			namedReading{"lower", status.TemperatureLowerC},
			namedReading{"combiner", status.TemperatureCombinerC},
		); temperature != nil && *temperature >= settings.SafetyStandbyTripC {
			blocked = append(blocked, "overtemperature-standby")
		}
	}
	return blocked
}

func normalizePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PolicyNormal:
		return PolicyNormal
	case PolicyHigh:
		return PolicyHigh
	default:
		return PolicyUnknown
	}
}

type namedReading struct {
	name  string
	value *float64
}

func maximumTemperature(readings ...namedReading) (*float64, string) {
	var maximum *float64
	name := ""
	for _, reading := range readings {
		if reading.value == nil || math.IsNaN(*reading.value) || math.IsInf(*reading.value, 0) {
			continue
		}
		if maximum == nil || *reading.value > *maximum {
			value := *reading.value
			maximum = &value
			name = reading.name
		}
	}
	return maximum, name
}
