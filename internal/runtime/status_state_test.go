package runtime

import (
	"reflect"
	"sync"
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

// The invalidation is generational, not time-based, and it lifts on the next
// published frame whatever that frame says. The repeat case is the subtle one:
// an amplifier sitting in a steady state answers every poll with identical
// bytes, so counting only changed frames would strand canonical status in the
// invalidated state for as long as nothing moved.
func TestStatusStateInvalidationLiftsOnTheNextPublishedFrameEvenIfUnchanged(t *testing.T) {
	polled := api.Status{Telemetry: api.Telemetry{
		OperatingState: "operate",
		TemperatureC:   floatPtr(42),
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}}
	display := Snapshot{
		Telemetry: api.Telemetry{
			Band:       "20m",
			Source:     "serial",
			Confidence: "display-derived",
			Provenance: "display-frame",
		},
		UpdatedAt: time.Now().UTC(),
	}

	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(polled)
	if resolved := state.Resolve(display); resolved.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll before any invalidation", resolved.Provenance)
	}

	state.InvalidatePreLeaseStatus()
	invalidated := state.Resolve(display)
	if invalidated.Provenance != "display-frame" || invalidated.TemperatureC != nil {
		t.Fatalf("invalidated status still carries the protocol frame: %+v", invalidated)
	}
	if invalidated.Band != "20m" {
		t.Fatalf("band = %q, want display-derived state to survive invalidation", invalidated.Band)
	}

	// Byte-identical to the frame that was invalidated: still fresh evidence.
	state.UpdateProtocolNative(polled)
	recovered := state.Resolve(display)
	if recovered.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll after an unchanged frame is republished", recovered.Provenance)
	}
	if recovered.TemperatureC == nil || *recovered.TemperatureC != 42 {
		t.Fatalf("temperature = %v, want 42 restored by the repeat frame", recovered.TemperatureC)
	}

	// Repeated invalidation without an intervening frame stays invalidated
	// rather than tripping over itself.
	state.InvalidatePreLeaseStatus()
	state.InvalidatePreLeaseStatus()
	if resolved := state.Resolve(display); resolved.Provenance != "display-frame" {
		t.Fatalf("provenance = %q, want display-frame while still invalidated", resolved.Provenance)
	}
}

// Authority and value are two different reasons for canonical status to move,
// and only one of them shows up in the retained bytes. A lease start invalidates
// a frame without touching it, and the first poll after a lease routinely
// restores authority with a byte-identical reply, so a fan-out keyed on changed
// bytes alone would leave every subscriber holding the wrong answer.
func TestStatusStateWakesSubscribersOnAuthorityTransitions(t *testing.T) {
	polled := api.Status{Telemetry: api.Telemetry{
		OperatingState: "operate",
		TemperatureC:   floatPtr(42),
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}}

	state := NewStatusState(api.Status{})
	updates, unsubscribe := state.Subscribe(1)
	defer unsubscribe()

	expectWake := func(t *testing.T, why string) {
		t.Helper()
		select {
		case <-updates:
		case <-time.After(2 * time.Second):
			t.Fatalf("no subscriber wake-up after %s", why)
		}
	}
	expectNoWake := func(t *testing.T, why string) {
		t.Helper()
		select {
		case got := <-updates:
			t.Fatalf("unexpected subscriber wake-up after %s: %+v", why, got)
		case <-time.After(150 * time.Millisecond):
		}
	}

	state.UpdateProtocolNative(polled)
	expectWake(t, "the first polled frame")

	// Losing authority changes what Resolve answers, so it has to be published
	// even though not one byte of the retained frame moved.
	state.InvalidatePreLeaseStatus()
	expectWake(t, "a lease invalidated the retained frame")

	// Invalidating what is already invalid answers the same as before.
	state.InvalidatePreLeaseStatus()
	expectNoWake(t, "a repeat invalidation with nothing published in between")

	// Byte-identical to the invalidated frame, and still the thing that makes
	// canonical status authoritative again.
	state.UpdateProtocolNative(polled)
	expectWake(t, "an unchanged frame lifted the invalidation")

	// With no invalidation in play an unchanged frame is genuinely nothing to
	// report, which is the pre-existing contract this must not widen.
	state.UpdateProtocolNative(polled)
	expectNoWake(t, "an unchanged frame while already authoritative")
}

// A status state that has never had a lease must behave exactly as before,
// including the seed value passed to NewStatusState.
func TestStatusStateSeedIsCanonicalUntilSomethingInvalidatesIt(t *testing.T) {
	seeded := NewStatusState(api.Status{Telemetry: api.Telemetry{
		ModelName:  "EXPERT 2K-FA",
		Provenance: "status-poll",
	}})

	if resolved := seeded.Resolve(Snapshot{}); resolved.ModelName != "EXPERT 2K-FA" {
		t.Fatalf("seeded status was not resolved: %+v", resolved)
	}
}

// A status websocket that goes away mid-fan-out removes and closes its own
// subscriber channel. If a publisher were to copy the subscriber set, release
// mu and only then send, that close can land in the gap and the send panics on
// a closed channel -- taking polling and monitoring down with the process. Both
// publishing paths are covered: a lease invalidating the retained frame, and an
// unchanged frame restoring authority afterwards, which is exactly the wake-up
// that has no changed bytes to hide behind.
//
// Run this under -race: the detector reports the close/send ordering even on the
// iterations where the timing does not actually produce a panic.
func TestStatusStateNotificationDoesNotRaceSubscriberRemoval(t *testing.T) {
	polled := api.Status{Telemetry: api.Telemetry{
		OperatingState: "operate",
		TemperatureC:   floatPtr(42),
		Source:         "serial",
		Confidence:     "protocol-native",
		Provenance:     "status-poll",
	}}

	cases := []struct {
		name string
		// arrange leaves the state in the condition where publish notifies.
		arrange func(*StatusState)
		publish func(*StatusState)
	}{
		{
			name: "lease invalidates the retained frame",
			arrange: func(state *StatusState) {
				state.UpdateProtocolNative(polled)
			},
			publish: func(state *StatusState) {
				state.InvalidatePreLeaseStatus()
			},
		},
		{
			name: "unchanged frame restores authority after a lease",
			arrange: func(state *StatusState) {
				state.UpdateProtocolNative(polled)
				state.InvalidatePreLeaseStatus()
			},
			publish: func(state *StatusState) {
				// Byte-identical to the retained frame: this wakes subscribers
				// only because it lifts the invalidation.
				state.UpdateProtocolNative(polled)
			},
		},
	}

	const (
		iterations  = 300
		subscribers = 8
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < iterations; i++ {
				state := NewStatusState(api.Status{})
				tc.arrange(state)

				unsubscribes := make([]func(), 0, subscribers)
				for j := 0; j < subscribers; j++ {
					_, unsubscribe := state.Subscribe(1)
					unsubscribes = append(unsubscribes, unsubscribe)
				}

				// Recovering here is test instrumentation, not a production
				// strategy: without it a send on a closed channel aborts the
				// whole package run with a bare stack trace, and the point of
				// this test is to name which publisher raced the disconnect.
				panicked := make(chan any, 1)
				start := make(chan struct{})
				var wg sync.WaitGroup

				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() {
						if r := recover(); r != nil {
							panicked <- r
						}
					}()
					<-start
					tc.publish(state)
				}()

				for _, unsubscribe := range unsubscribes {
					wg.Add(1)
					go func(unsubscribe func()) {
						defer wg.Done()
						<-start
						unsubscribe()
					}(unsubscribe)
				}

				close(start)
				wg.Wait()

				select {
				case r := <-panicked:
					t.Fatalf("publishing raced a concurrent unsubscribe on iteration %d: %v", i, r)
				default:
				}
			}
		})
	}
}

// Unsubscribing twice must stay harmless: the second call finds the channel
// already removed and must not close it again, which is its own panic.
func TestStatusStateUnsubscribeIsIdempotent(t *testing.T) {
	state := NewStatusState(api.Status{})
	updates, unsubscribe := state.Subscribe(1)

	unsubscribe()
	unsubscribe()

	if _, ok := <-updates; ok {
		t.Fatal("expected the unsubscribed channel to be closed")
	}
}

func floatPtr(v float64) *float64 {
	return &v
}

// TestStatusStateExpiresStaleTappedStatusBackToDisplayDerived pins the exact
// case reported against d1803a5: a tapped frame that stopped being refreshed
// outranking newer display telemetry, and borrowing that newer telemetry's
// timestamp to call itself current.
//
// The lease shape this comes from is the measured one, not a hypothetical --
// Expert Controller Plus never sends 0x90, so a tap can fall silent for the
// whole session while display frames keep arriving at full rate.
func TestStatusStateExpiresStaleTappedStatusBackToDisplayDerived(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		Provenance:         "status-poll",
		Source:             "serial",
		Confidence:         "protocol-native",
		TemperatureC:       floatPtr(40),
		TemperatureDisplay: "40 C",
	}})
	// One tapped frame lands as the lease begins, then nothing more.
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		Provenance:         ProvenancePassthroughTap,
		Source:             "serial",
		Confidence:         "protocol-native",
		TemperatureC:       floatPtr(40),
		TemperatureDisplay: "40 C",
	}})

	freshDisplay := Snapshot{
		UpdatedAt: time.Now().UTC(),
		Telemetry: api.Telemetry{
			Provenance:         "display-frame",
			Source:             "serial",
			Confidence:         "display-derived",
			TemperatureC:       floatPtr(80),
			TemperatureDisplay: "80 C",
		},
	}

	// While the tap is still being refreshed it legitimately speaks.
	if status := state.Resolve(freshDisplay); status.Provenance != ProvenancePassthroughTap {
		t.Fatalf("fresh tap provenance = %q, want %q", status.Provenance, ProvenancePassthroughTap)
	}

	// Age the tap past its window. Setting the field directly is what keeps this
	// exact and instant; the alternative is sleeping out RecentContactWindow.
	state.mu.Lock()
	state.lastProtocolAt = time.Now().UTC().Add(-(RecentContactWindow + time.Second))
	state.mu.Unlock()

	status := state.Resolve(freshDisplay)
	if status.Provenance == ProvenancePassthroughTap {
		t.Fatalf("stale tap still canonical: provenance = %q", status.Provenance)
	}
	if status.Provenance != "display-frame" {
		t.Fatalf("provenance = %q, want display-frame", status.Provenance)
	}
	if status.TemperatureC == nil || *status.TemperatureC != 80 {
		t.Fatalf("temperatureC = %v, want the fresh display-derived 80", status.TemperatureC)
	}
	if status.TemperatureDisplay != "80 C" {
		t.Fatalf("temperatureDisplay = %q, want %q", status.TemperatureDisplay, "80 C")
	}
	// recentContact may still be true here, and correctly so: it is now saying
	// the display is being refreshed, which it is. What it must never do is
	// report freshness on behalf of a reading the tap supplied and abandoned.
	if !status.RecentContact {
		t.Fatal("recentContact = false, want true from the fresh display snapshot")
	}
}

// TestStatusStateKeepsTappedStatusWhileTheTapIsStillFresh guards the other
// direction, so the expiry above cannot be "fixed" by dropping tapped status
// altogether -- during a lease a live tap is the best evidence there is.
func TestStatusStateKeepsTappedStatusWhileTheTapIsStillFresh(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		Provenance:   ProvenancePassthroughTap,
		Source:       "serial",
		Confidence:   "protocol-native",
		TemperatureC: floatPtr(40),
	}})

	status := state.Resolve(Snapshot{
		UpdatedAt: time.Now().UTC(),
		Telemetry: api.Telemetry{Provenance: "display-frame", TemperatureC: floatPtr(80)},
	})
	if status.Provenance != ProvenancePassthroughTap {
		t.Fatalf("provenance = %q, want %q", status.Provenance, ProvenancePassthroughTap)
	}
	if status.TemperatureC == nil || *status.TemperatureC != 40 {
		t.Fatalf("temperatureC = %v, want the live tapped 40", status.TemperatureC)
	}
}

// TestStatusStateDoesNotExpireStatusPollOnTapStaleness pins the scope of the
// expiry. A status-poll frame is refreshed by the server's own polling and its
// age is already reported truthfully by recentContact; expiring it here would
// change normal non-lease behavior, which is not what the tap fix is for.
func TestStatusStateDoesNotExpireStatusPollOnTapStaleness(t *testing.T) {
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{
		Provenance:   "status-poll",
		Source:       "serial",
		Confidence:   "protocol-native",
		TemperatureC: floatPtr(40),
	}})

	state.mu.Lock()
	state.lastProtocolAt = time.Now().UTC().Add(-(RecentContactWindow + time.Second))
	state.mu.Unlock()

	status := state.Resolve(Snapshot{
		UpdatedAt: time.Now().UTC(),
		Telemetry: api.Telemetry{Provenance: "display-frame", TemperatureC: floatPtr(80)},
	})
	if status.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll to be unaffected", status.Provenance)
	}
	if status.TemperatureC == nil || *status.TemperatureC != 40 {
		t.Fatalf("temperatureC = %v, want the retained status-poll 40", status.TemperatureC)
	}
}
