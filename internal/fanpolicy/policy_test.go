package fanpolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

func TestEvaluateDisabled(t *testing.T) {
	result := Evaluate(statusAt(90, "standby", false), Settings{
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	}, "")
	if result.State != StateDisabled || result.Pending || result.ActionAvailable {
		t.Fatalf("unexpected disabled result: %+v", result)
	}
}

func TestEvaluateUsesHottestSensorAndAllowsOperateRXAtUpperThreshold(t *testing.T) {
	status := statusAt(79, "operate", false)
	lower := 81.0
	status.TemperatureLowerC = &lower
	result := Evaluate(status, Settings{
		Enabled:            true,
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	}, PolicyNormal)
	if result.State != StatePending || result.DesiredPolicy != PolicyHigh || !result.Pending {
		t.Fatalf("unexpected high-cooling result: %+v", result)
	}
	if result.Observations.MaximumTemperatureC == nil || *result.Observations.MaximumTemperatureC != 81 || result.Observations.TemperatureSensor != "lower" {
		t.Fatalf("unexpected temperature observation: %+v", result.Observations)
	}
	if len(result.BlockedBy) != 0 {
		t.Fatalf("BlockedBy = %v", result.BlockedBy)
	}
}

func TestEvaluateRequestsNormalAtLowerThreshold(t *testing.T) {
	result := Evaluate(statusAt(75, "standby", false), Settings{
		Enabled:            true,
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	}, PolicyHigh)
	if result.DesiredPolicy != PolicyNormal || result.State != StatePending || result.ActionAvailable {
		t.Fatalf("unexpected normal result: %+v", result)
	}
	if result.Reason != "fan-policy change is waiting for a verified home display" {
		t.Fatalf("Reason = %q", result.Reason)
	}
}

func TestEvaluatePreservesDesiredPolicyInsideHysteresisBand(t *testing.T) {
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	for _, policy := range []string{PolicyNormal, PolicyHigh} {
		result := Evaluate(statusAt(77, "standby", false), settings, policy)
		if result.DesiredPolicy != policy {
			t.Fatalf("previous %q became %q", policy, result.DesiredPolicy)
		}
	}
}

func TestEvaluateDoesNotInventPolicyInsideHysteresisBand(t *testing.T) {
	result := Evaluate(statusAt(77, "standby", false), Settings{
		Enabled:            true,
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	}, "")
	if result.State != StateNormal || result.DesiredPolicy != PolicyUnknown || result.Pending {
		t.Fatalf("unexpected hysteresis result: %+v", result)
	}
}

func TestEvaluateRequiresRecentProtocolNativeStatus(t *testing.T) {
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	stale := statusAt(90, "standby", false)
	stale.RecentContact = false
	display := statusAt(90, "standby", false)
	display.Provenance = "display-frame"
	for _, status := range []api.Status{stale, display} {
		result := Evaluate(status, settings, "")
		if result.State != StateUnavailable || result.Pending {
			t.Fatalf("unexpected unavailable result: %+v", result)
		}
	}
}

func TestEvaluateWaitsForExplicitRX(t *testing.T) {
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	unknown := statusAt(90, "standby", false)
	unknown.TX = nil
	result := Evaluate(unknown, settings, "")
	if len(result.BlockedBy) != 1 || result.BlockedBy[0] != "known-rx" {
		t.Fatalf("unknown TX blockedBy = %v", result.BlockedBy)
	}
	transmitting := statusAt(90, "standby", true)
	result = Evaluate(transmitting, settings, "")
	if len(result.BlockedBy) != 1 || result.BlockedBy[0] != "rx" {
		t.Fatalf("TX blockedBy = %v", result.BlockedBy)
	}
}

func TestEvaluateRejectsUnknownOperatingStateButAllowsOperateAndStandby(t *testing.T) {
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	for _, operatingState := range []string{"operate", "standby"} {
		result := Evaluate(statusAt(90, operatingState, false), settings, "")
		if containsBlock(result.BlockedBy, "operating-state") {
			t.Fatalf("%s was incorrectly blocked: %+v", operatingState, result)
		}
	}
	result := Evaluate(statusAt(90, "unknown", false), settings, "")
	if !containsBlock(result.BlockedBy, "operating-state") {
		t.Fatalf("unknown operating state was not blocked: %+v", result)
	}
}

func TestEvaluateRequiresReviewedFanProfileBeforeNavigation(t *testing.T) {
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	status := statusAt(90, "standby", false)
	status.ModelName = "EXPERT 2K-FA"
	result := Evaluate(status, settings, "")
	if !containsBlock(result.BlockedBy, "supported-fan-profile") || result.State != StateBlocked {
		t.Fatalf("uncaptured Expert profile was not blocked before SET: %+v", result)
	}
	status.ModelName = "OTHER AMP"
	result = Evaluate(status, settings, "")
	if !containsBlock(result.BlockedBy, "supported-fan-profile") {
		t.Fatalf("non-Expert profile was not blocked: %+v", result)
	}
}

func TestEvaluateAdvertisesModesOnlyForPromotedProfiles(t *testing.T) {
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	for _, model := range []string{"EXPERT 1.3K-FA", "EXPERT 1.5K-FA"} {
		status := statusAt(90, "standby", false)
		status.ModelName = model
		result := Evaluate(status, settings, "")
		if got := strings.Join(result.SupportedModes, ","); got != "normal,contest" {
			t.Fatalf("%s supported modes = %q", model, got)
		}
	}
	status := statusAt(90, "standby", false)
	status.ModelName = "EXPERT F-KFA"
	if modes := Evaluate(status, settings, "").SupportedModes; len(modes) != 0 {
		t.Fatalf("unsupported model advertised modes: %v", modes)
	}

	status.ModelName = "EXPERT 2K-FA"
	for _, firmware := range []string{ThirdSeriesEvidenceFirmware, ThirdSeriesCurrentFirmware} {
		settings.FirmwareVersion = firmware
		if got := strings.Join(Evaluate(status, settings, "").SupportedModes, ","); got != "normal,contest" {
			t.Fatalf("Third Series firmware %q supported modes = %q", firmware, got)
		}
	}
	for _, firmware := range []string{"", "Rel.08_06_26", "rel.08_06_26_a", "Rel.08_06_26_B"} {
		settings.FirmwareVersion = firmware
		result := Evaluate(status, settings, "")
		if len(result.SupportedModes) != 0 || !containsBlock(result.BlockedBy, "supported-fan-profile") {
			t.Fatalf("unsupported Third Series firmware %q was advertised or unblocked: %+v", firmware, result)
		}
	}
}

func statusAt(temperature float64, operatingState string, tx bool) api.Status {
	return api.Status{
		Telemetry: api.Telemetry{
			TemperatureC:   &temperature,
			ModelName:      "EXPERT 1.3K-FA",
			OperatingState: operatingState,
			TX:             &tx,
			Provenance:     "status-poll",
		},
		RecentContact: true,
		LastContactAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}
