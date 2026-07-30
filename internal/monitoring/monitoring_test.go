package monitoring

import (
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

func TestEvaluateDisabledStillReportsObservations(t *testing.T) {
	temp := 42.0
	result := Evaluate(api.Status{
		Telemetry: api.Telemetry{TemperatureC: &temp},
	}, false, Thresholds{})

	if result.State != StateDisabled || result.Observations.MaximumTemperatureC == nil || *result.Observations.MaximumTemperatureC != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.DryRun || len(result.ActionsTaken) != 0 {
		t.Fatalf("monitor must remain dry-run: %+v", result)
	}
}

func TestEvaluateUsesHottestTemperatureSensor(t *testing.T) {
	upper, lower, combiner := 60.0, 75.0, 70.0
	result := Evaluate(api.Status{
		Telemetry: api.Telemetry{
			TemperatureC:         &upper,
			TemperatureLowerC:    &lower,
			TemperatureCombinerC: &combiner,
		},
		RecentContact: true,
	}, true, Thresholds{TemperatureWarningC: 70, TemperatureTripC: 80})

	if result.State != StateWarning {
		t.Fatalf("State = %q, want warning: %+v", result.State, result)
	}
	if result.Observations.TemperatureSensor != "lower" || result.Observations.MaximumTemperatureC == nil || *result.Observations.MaximumTemperatureC != 75 {
		t.Fatalf("unexpected observations: %+v", result.Observations)
	}
}

func TestEvaluateSWRIsIdleOutsideTX(t *testing.T) {
	swr := 9.0
	tx := false
	result := Evaluate(api.Status{
		Telemetry:     api.Telemetry{SWR: &swr, TX: &tx},
		RecentContact: true,
	}, true, Thresholds{SWRWarning: 2, SWRTrip: 3})

	if result.State != StateIdle || len(result.Conditions) != 1 || result.Conditions[0].Evaluated {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestEvaluateSWRTripDuringTXUsesMaximumReading(t *testing.T) {
	atu, antenna := 1.4, 3.2
	tx := true
	result := Evaluate(api.Status{
		Telemetry:     api.Telemetry{SWR: &atu, AntennaSWR: &antenna, TX: &tx},
		RecentContact: true,
	}, true, Thresholds{SWRWarning: 2, SWRTrip: 3})

	if result.State != StateTrip || result.Observations.SWRSource != "antenna" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.ActionsTaken) != 0 {
		t.Fatalf("dry-run monitor took actions: %+v", result.ActionsTaken)
	}
}

func TestEvaluateUnavailableWhenContactIsStale(t *testing.T) {
	result := Evaluate(api.Status{}, true, Thresholds{TemperatureWarningC: 70, TemperatureTripC: 80})
	if result.State != StateUnavailable || result.Reason != "amplifier status is not recent" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
