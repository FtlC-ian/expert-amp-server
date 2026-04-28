package runtime

import (
	"reflect"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

func TestStatusStateFallsBackToRuntimeSnapshotWhenNoProtocolNativeStatus(t *testing.T) {
	state := NewStatusState(api.Status{})
	snapshot := Snapshot{Telemetry: api.Telemetry{
		Band:           "20m",
		OperatingState: "standby",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
	}}

	status := state.Resolve(snapshot)
	if status.Band != "20m" || status.Provenance != "display-frame" {
		t.Fatalf("unexpected fallback status: %+v", status)
	}
}

func TestStatusStateResolveNilReceiverFallsBackToSnapshot(t *testing.T) {
	var state *StatusState
	snapshot := Snapshot{Telemetry: api.Telemetry{
		Band:       "40m",
		Provenance: "display-frame",
	}}

	status := state.Resolve(snapshot)
	if status.Band != "40m" || status.Provenance != "display-frame" {
		t.Fatalf("unexpected fallback status: %+v", status)
	}
}

func TestStatusStatePrefersProtocolNativeStatusPollState(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		ModelName:      "EXPERT 2K-FA",
		OperatingState: "operate",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}, BandCode: "00", BandText: "160m"})
	snapshot := Snapshot{Telemetry: api.Telemetry{
		Band:           "20m",
		OperatingState: "standby",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
	}}

	status := state.Resolve(snapshot)
	if status.Provenance != "status-poll" || status.ModelName != "EXPERT 2K-FA" || status.BandCode != "00" || status.BandText != "160m" {
		t.Fatalf("unexpected protocol-native status: %+v", status)
	}
	if status.Band != "" {
		t.Fatalf("band = %q, want empty when protocol-native bandText is available", status.Band)
	}
}

func TestStatusStateMergesProtocolNativeStatusWithDisplayFallback(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState:          "operate",
		Source:                  "serial",
		Confidence:              "protocol-native",
		Provenance:              "status-poll",
		TX:                      boolPtr(true),
		AntennaSWRDisplay:       "1.20",
		PASupplyVoltageDisplay:  "48.0",
		PACurrentDisplay:        "12.5",
		TemperatureLowerDisplay: "33",
	}, BandCode: "09", BandText: "10m", ATUStatusCode: "b"})
	snapshot := Snapshot{Telemetry: api.Telemetry{
		ModelName:               "EXPERT 1.3K-FA",
		Band:                    "6m",
		Input:                   "2",
		Antenna:                 "4b",
		OutputLevel:             "LOW",
		AntennaSWRDisplay:       "1.10",
		PASupplyVoltageDisplay:  "47.5",
		PACurrentDisplay:        "10.0",
		TemperatureLowerDisplay: "30",
		Source:                  "serial",
		Confidence:              "display-derived",
		Provenance:              "display-frame",
	}}

	status := state.Resolve(snapshot)
	if status.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll", status.Provenance)
	}
	if status.ModelName != "EXPERT 1.3K-FA" {
		t.Fatalf("modelName = %q, want display fallback model", status.ModelName)
	}
	if status.Band != "" || status.BandText != "10m" || status.Input != "2" || status.Antenna != "4b" || status.OutputLevel != "LOW" {
		t.Fatalf("expected merged display fields with protocol-native band text, got %+v", status)
	}
	if status.TX == nil || !*status.TX {
		t.Fatalf("tx = %v, want true from protocol-native status", status.TX)
	}
	if status.ATUStatusCode != "b" || status.AntennaSWRDisplay != "1.20" || status.PASupplyVoltageDisplay != "48.0" || status.PACurrentDisplay != "12.5" || status.TemperatureLowerDisplay != "33" {
		t.Fatalf("expected protocol-native promoted fields to win, got %+v", status)
	}
}

func TestStatusStateAddsRecentContactMetadataFromSnapshotOrStatusPoll(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{Provenance: "status-poll"}})
	snapshot := Snapshot{UpdatedAt: time.Now().UTC().Add(-10 * time.Second)}

	status := state.Resolve(snapshot)
	if !status.RecentContact {
		t.Fatalf("RecentContact = false, want true")
	}
	if status.LastContactAt == "" {
		t.Fatal("LastContactAt empty, want timestamp")
	}

	stale := applyContactMetadata(api.Status{}, time.Now().UTC().Add(-(RecentContactWindow + time.Second)), time.Time{})
	if stale.RecentContact {
		t.Fatalf("RecentContact = true, want false for stale snapshot")
	}
}

func TestStatusStateSubscribePublishesRealProtocolChangesOnly(t *testing.T) {
	state := NewStatusState(api.Status{})
	updates, unsubscribe := state.Subscribe(1)
	defer unsubscribe()

	base := api.Status{Telemetry: api.Telemetry{Provenance: "status-poll", OperatingState: "standby"}}
	state.UpdateProtocolNative(base)
	first := <-updates
	if first.OperatingState != "standby" {
		t.Fatalf("unexpected first update: %+v", first)
	}

	state.UpdateProtocolNative(base)
	select {
	case duplicate := <-updates:
		t.Fatalf("unexpected duplicate update: %+v", duplicate)
	case <-time.After(150 * time.Millisecond):
	}

	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{Provenance: "status-poll", OperatingState: "operate"}})
	second := <-updates
	if second.OperatingState != "operate" {
		t.Fatalf("unexpected second update: %+v", second)
	}
}

func TestMergeProtocolNativeStatusFillsEveryZeroFieldFromFallback(t *testing.T) {
	fallback := api.Status{
		Telemetry: api.Telemetry{
			ModelName:                  "EXPERT 2K-FA",
			OperatingState:             "operate",
			Mode:                       "operate",
			TX:                         boolPtr(true),
			Band:                       "20m",
			Input:                      "2",
			Antenna:                    "4b",
			AntennaBank:                "A",
			CATInterface:               "CAT",
			CATMode:                    "radio",
			OutputLevel:                "HIGH",
			SWR:                        floatPtr(1.1),
			SWRDisplay:                 "1.10",
			AntennaSWR:                 floatPtr(1.2),
			AntennaSWRDisplay:          "1.20",
			PASupplyVoltage:            floatPtr(48.5),
			PASupplyVoltageDisplay:     "48.5",
			PACurrent:                  floatPtr(12.3),
			PACurrentDisplay:           "12.3",
			TemperatureC:               floatPtr(37),
			TemperatureDisplay:         "37",
			TemperatureLowerC:          floatPtr(33),
			TemperatureLowerDisplay:    "33",
			TemperatureCombinerC:       floatPtr(35),
			TemperatureCombinerDisplay: "35",
			Frequency:                  "14.074",
			PowerWatts:                 floatPtr(1000),
			Source:                     "serial",
			Confidence:                 "display-derived",
			Provenance:                 "display-frame",
			Notes:                      []string{"fallback note"},
		},
		RecentContact: true,
		LastContactAt: "2026-04-21T20:00:00Z",
		BandCode:      "05",
		BandText:      "20m",
		RXAntenna:     "rx1",
		WarningCode:   "w",
		AlarmCode:     "a",
		ATUStatusCode: "b",
		WarningsText:  []string{"warning"},
		AlarmsText:    []string{"alarm"},
		Warnings:      []string{"warning-code"},
		ActiveAlarms:  []string{"alarm-code"},
	}

	merged := mergeProtocolNativeStatus(api.Status{}, fallback)
	if !reflect.DeepEqual(merged, fallback) {
		t.Fatalf("merged status mismatch\n got: %+v\nwant: %+v", merged, fallback)
	}
}

func TestMergeProtocolNativeStatusKeepsBandEmptyWhenBandTextExists(t *testing.T) {
	merged := mergeProtocolNativeStatus(api.Status{BandText: "160m", Telemetry: api.Telemetry{Provenance: "status-poll"}}, api.Status{Telemetry: api.Telemetry{Band: "20m"}})
	if merged.Band != "" {
		t.Fatalf("Band = %q, want empty when protocol-native band text exists", merged.Band)
	}
}

func TestMergeProtocolNativeStatusPartiallyMergesEmbeddedTelemetry(t *testing.T) {
	protocol := api.Status{Telemetry: api.Telemetry{
		ModelName:  "EXPERT 1.3K-FA",
		Provenance: "status-poll",
	}}
	fallback := api.Status{Telemetry: api.Telemetry{
		Band:           "20m",
		OperatingState: "standby",
		Source:         "serial",
		Confidence:     "display-derived",
		Provenance:     "display-frame",
	}}

	merged := mergeProtocolNativeStatus(protocol, fallback)
	if merged.ModelName != "EXPERT 1.3K-FA" {
		t.Fatalf("ModelName = %q, want protocol-native value", merged.ModelName)
	}
	if merged.Band != "20m" || merged.OperatingState != "standby" || merged.Source != "serial" || merged.Confidence != "display-derived" {
		t.Fatalf("expected fallback telemetry fields to fill zero embedded fields, got %+v", merged)
	}
	if merged.Provenance != "status-poll" {
		t.Fatalf("Provenance = %q, want protocol-native value", merged.Provenance)
	}
}

func TestStatusStatePrefersFresherDisplayStateForLaggyDisplayOnlyOperatorFields(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "standby",
		Mode:           "standby",
		OutputLevel:    "LOW",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})

	time.Sleep(10 * time.Millisecond)
	snapshot := Snapshot{
		Telemetry: api.Telemetry{
			OperatingState: "operate",
			Mode:           "operate",
			OutputLevel:    "HIGH",
			Source:         "serial",
			Confidence:     "display-derived",
			Provenance:     "display-frame",
		},
		UpdatedAt: time.Now().UTC(),
	}

	status := state.Resolve(snapshot)
	if status.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll", status.Provenance)
	}
	if status.OperatingState != "operate" || status.Mode != "operate" {
		t.Fatalf("expected fresher display overrides for laggy display-only fields, got %+v", status)
	}
	if status.OutputLevel != "LOW" {
		t.Fatalf("outputLevel = %q, want protocol-native LOW despite newer display text", status.OutputLevel)
	}
}

func TestStatusStateIgnoresNonCanonicalFresherDisplayOperatingText(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "standby",
		Mode:           "standby",
		OutputLevel:    "LOW",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})

	time.Sleep(10 * time.Millisecond)
	status := state.Resolve(Snapshot{
		Telemetry: api.Telemetry{
			OperatingState: "SET ANTENNA ON BANK A",
			Mode:           "DISPLAY ALARMS LOG EXIT",
			Source:         "serial",
			Confidence:     "display-derived",
			Provenance:     "display-frame",
		},
		UpdatedAt: time.Now().UTC(),
	})
	if status.OperatingState != "standby" || status.Mode != "standby" {
		t.Fatalf("non-canonical display text overrode protocol state: %+v", status)
	}
}

func TestStatusStateUsesDisplayOutputLevelOnlyWhenStatusPollDoesNotReportIt(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "standby",
		Mode:           "standby",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})

	time.Sleep(10 * time.Millisecond)
	snapshot := Snapshot{
		Telemetry: api.Telemetry{
			OutputLevel: "HIGH",
			Source:      "serial",
			Confidence:  "display-derived",
			Provenance:  "display-frame",
		},
		UpdatedAt: time.Now().UTC(),
	}

	status := state.Resolve(snapshot)
	if status.OutputLevel != "HIGH" {
		t.Fatalf("outputLevel = %q, want display fallback HIGH when protocol-native status lacks outputLevel", status.OutputLevel)
	}
}

func TestStatusStateDoesNotLetFresherDisplayEchoWobbleProtocolOutputLevel(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "operate",
		Mode:           "operate",
		OutputLevel:    "LOW",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})

	for _, echoed := range []string{"MID", "HIGH", "MAX"} {
		time.Sleep(10 * time.Millisecond)
		status := state.Resolve(Snapshot{
			Telemetry: api.Telemetry{
				OperatingState: "operate",
				Mode:           "operate",
				OutputLevel:    echoed,
				Source:         "serial",
				Confidence:     "display-derived",
				Provenance:     "display-frame",
			},
			UpdatedAt: time.Now().UTC(),
		})
		if status.OutputLevel != "LOW" {
			t.Fatalf("display echo %q changed canonical outputLevel to %q, want protocol-native LOW", echoed, status.OutputLevel)
		}
	}

	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "operate",
		Mode:           "operate",
		OutputLevel:    "HIGH",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})
	status := state.Resolve(Snapshot{Telemetry: api.Telemetry{OutputLevel: "MAX", Provenance: "display-frame"}, UpdatedAt: time.Now().UTC()})
	if status.OutputLevel != "HIGH" {
		t.Fatalf("outputLevel = %q after real protocol-native change, want HIGH", status.OutputLevel)
	}
}

func TestStatusStateKeepsProtocolValuesWhenDisplaySnapshotTimestampTiesProtocol(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "standby",
		Mode:           "standby",
		OutputLevel:    "LOW",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})

	protocolAt := state.protocolUpdatedAt()
	snapshot := Snapshot{
		Telemetry: api.Telemetry{
			OperatingState: "operate",
			Mode:           "operate",
			OutputLevel:    "HIGH",
			Source:         "serial",
			Confidence:     "display-derived",
			Provenance:     "display-frame",
		},
		UpdatedAt: protocolAt,
	}

	status := state.Resolve(snapshot)
	if status.OperatingState != "standby" || status.Mode != "standby" || status.OutputLevel != "LOW" {
		t.Fatalf("expected protocol values to win on timestamp tie, got %+v", status)
	}
}

func TestStatusStateKeepsProtocolValuesWhenDisplaySnapshotIsNotNewer(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		OperatingState: "operate",
		Mode:           "operate",
		OutputLevel:    "HIGH",
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}})

	snapshot := Snapshot{
		Telemetry: api.Telemetry{
			OperatingState: "standby",
			Mode:           "standby",
			OutputLevel:    "LOW",
			Source:         "serial",
			Confidence:     "display-derived",
			Provenance:     "display-frame",
		},
	}

	status := state.Resolve(snapshot)
	if status.OperatingState != "operate" || status.Mode != "operate" || status.OutputLevel != "HIGH" {
		t.Fatalf("expected protocol-native values to win without fresher display evidence, got %+v", status)
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
