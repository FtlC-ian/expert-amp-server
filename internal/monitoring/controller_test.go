package monitoring

import (
	"context"
	"errors"
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/fanpolicy"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

type recordingTransport struct {
	actions []api.ButtonAction
	err     error
}

func (t *recordingTransport) SendButton(_ context.Context, action api.ButtonAction) (api.ActionResult, error) {
	t.actions = append(t.actions, action)
	return api.ActionResult{Name: action.Name, Sent: t.err == nil}, t.err
}

func TestControllerRequestsStandbyExactlyOncePerLatchedEpisode(t *testing.T) {
	buttons := &recordingTransport{}
	controller := NewController(buttons)
	settings := armedSettings()
	status := tripStatus(46)

	controller.Observe(context.Background(), status, settings)
	controller.Observe(context.Background(), status, settings)

	if len(buttons.actions) != 1 || buttons.actions[0].Name != "operate" {
		t.Fatalf("actions = %+v, want exactly one operate toggle", buttons.actions)
	}
	result := controller.Apply(Evaluate(status, true, settings.Thresholds), true)
	if !result.Latched || result.Action.State != ActionUnconfirmed || result.Action.RequestedAt == "" || result.Action.CompletedAt == "" {
		t.Fatalf("unexpected action state: %+v", result)
	}

	controller.Observe(context.Background(), tripStatus(39), settings)
	if controller.Apply(Evaluate(tripStatus(39), true, settings.Thresholds), true).Latched {
		t.Fatal("controller stayed latched below reset threshold")
	}
	controller.Observe(context.Background(), status, settings)
	if len(buttons.actions) != 2 {
		t.Fatalf("actions after reset = %d, want 2", len(buttons.actions))
	}
}

func TestControllerNeverRetriesFailedOrAmbiguousWrite(t *testing.T) {
	buttons := &recordingTransport{err: errors.New("serial write failed")}
	controller := NewController(buttons)
	settings := armedSettings()
	status := tripStatus(46)

	controller.Observe(context.Background(), status, settings)
	controller.Observe(context.Background(), status, settings)

	result := controller.Apply(Evaluate(status, true, settings.Thresholds), true)
	if len(buttons.actions) != 1 || !result.Latched || result.Action.State != ActionFailed || result.Action.Error == "" {
		t.Fatalf("unexpected failed action result: actions=%+v result=%+v", buttons.actions, result)
	}
}

func TestControllerRequiresArmingRecentProtocolOperateExplicitRXAndTrip(t *testing.T) {
	tests := []struct {
		name     string
		status   api.Status
		settings ControlSettings
	}{
		{name: "not armed", status: tripStatus(46), settings: func() ControlSettings { s := armedSettings(); s.Armed = false; return s }()},
		{name: "stale", status: func() api.Status { s := tripStatus(46); s.RecentContact = false; return s }(), settings: armedSettings()},
		{name: "display derived", status: func() api.Status { s := tripStatus(46); s.Provenance = "display-frame"; return s }(), settings: armedSettings()},
		{name: "standby", status: func() api.Status { s := tripStatus(46); s.OperatingState = "standby"; return s }(), settings: armedSettings()},
		{name: "tx", status: func() api.Status { s := tripStatus(46); tx := true; s.TX = &tx; return s }(), settings: armedSettings()},
		{name: "unknown rx tx", status: func() api.Status { s := tripStatus(46); s.TX = nil; return s }(), settings: armedSettings()},
		{name: "below trip", status: tripStatus(44), settings: armedSettings()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buttons := &recordingTransport{}
			NewController(buttons).Observe(context.Background(), tc.status, tc.settings)
			if len(buttons.actions) != 0 {
				t.Fatalf("actions = %+v, want none", buttons.actions)
			}
		})
	}
}

func TestControllerDoesNotClaimConfirmationFromLaterStandbyObservation(t *testing.T) {
	controller := NewController(&recordingTransport{})
	settings := armedSettings()
	controller.Observe(context.Background(), tripStatus(46), settings)

	standby := tripStatus(46)
	standby.OperatingState = "standby"
	controller.Observe(context.Background(), standby, settings)

	result := controller.Apply(Evaluate(standby, true, settings.Thresholds), true)
	if result.Action.State != ActionUnconfirmed || result.Action.Observation == "" {
		t.Fatalf("unexpected action state: %+v", result.Action)
	}
}

func TestControllerDoesNotRetryWhenNextPollStillReportsOperate(t *testing.T) {
	buttons := &recordingTransport{}
	controller := NewController(buttons)
	settings := armedSettings()
	status := tripStatus(46)
	controller.Observe(context.Background(), status, settings)
	controller.Observe(context.Background(), status, settings)

	result := controller.Apply(Evaluate(status, true, settings.Thresholds), true)
	if len(buttons.actions) != 1 || result.Action.State != ActionUnconfirmed || result.Action.Observation == "" {
		t.Fatalf("unexpected unconfirmed state: actions=%+v action=%+v", buttons.actions, result.Action)
	}
}

func TestControllerReportsContactLossWithoutClaimingConfirmation(t *testing.T) {
	controller := NewController(&recordingTransport{})
	settings := armedSettings()
	status := tripStatus(46)
	controller.Observe(context.Background(), status, settings)
	controller.ObserveContactLoss()

	result := controller.Apply(Evaluate(status, true, settings.Thresholds), true)
	if result.Action.State != ActionUnconfirmed || result.Action.Observation == "" {
		t.Fatalf("unexpected contact-loss state: %+v", result.Action)
	}
}

func TestOvertemperatureSafetyPreemptsFanPolicyAtSameTemperatureSample(t *testing.T) {
	raw := &recordingTransport{}
	coordinator := transport.NewActuationCoordinator(raw)
	safety := NewController(coordinator.Owner(transport.ActuationOwnerSafety, true))
	fan := fanpolicy.NewController(coordinator.Owner(transport.ActuationOwnerFan, false))
	status := tripStatus(46)

	safety.Observe(context.Background(), status, armedSettings())
	fanSettings := fanpolicy.Settings{
		Enabled:            true,
		DisplayProfile:     fanpolicy.SupportedDisplayProfile,
		HighTemperatureC:   42,
		NormalTemperatureC: 37,
		SafetyStandbyArmed: true,
		SafetyStandbyTripC: 45,
	}
	fan.Observe(status, fanSettings)
	tx := false
	operate := true
	fan.ObserveDisplay(fanpolicy.DisplayObservation{
		State:      display.NewState(),
		Generation: 1,
		TX:         &tx,
		Operate:    &operate,
	})

	if len(raw.actions) != 1 || raw.actions[0].Name != "operate" {
		t.Fatalf("actions = %+v, want exactly one safety operate toggle", raw.actions)
	}
	if result := fan.Current(); !contains(result.BlockedBy, "overtemperature-standby") {
		t.Fatalf("fan policy was not blocked by safety trip: %+v", result)
	}
}

func TestSafetyHoldReleasesWhenProtectionIsDisarmed(t *testing.T) {
	raw := &recordingTransport{}
	coordinator := transport.NewActuationCoordinator(raw)
	controller := NewController(coordinator.Owner(transport.ActuationOwnerSafety, true))
	settings := armedSettings()
	controller.Observe(context.Background(), tripStatus(46), settings)

	settings.Armed = false
	controller.Observe(context.Background(), tripStatus(46), settings)
	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err != nil {
		t.Fatalf("manual recovery remained blocked after disarm: %v", err)
	}
}

func TestSafetyHoldReleasesAfterFailedOneShot(t *testing.T) {
	raw := &recordingTransport{err: errors.New("serial write failed")}
	coordinator := transport.NewActuationCoordinator(raw)
	controller := NewController(coordinator.Owner(transport.ActuationOwnerSafety, true))
	controller.Observe(context.Background(), tripStatus(46), armedSettings())

	raw.err = nil
	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err != nil {
		t.Fatalf("manual recovery remained blocked after failed safety write: %v", err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func armedSettings() ControlSettings {
	return ControlSettings{
		Enabled: true,
		Armed:   true,
		Thresholds: Thresholds{
			TemperatureWarningC: 40,
			TemperatureTripC:    45,
			TemperatureResetC:   40,
		},
	}
}

func tripStatus(temperature float64) api.Status {
	tx := false
	return api.Status{
		Telemetry: api.Telemetry{
			OperatingState: "operate",
			TX:             &tx,
			TemperatureC:   &temperature,
			Provenance:     "status-poll",
		},
		RecentContact: true,
	}
}
