package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

func TestStoreApplyIncrementsSequenceOnChange(t *testing.T) {
	store := NewStore(Snapshot{})
	state := display.DemoState()
	telemetry := api.Telemetry{Band: "20m", Source: "fixture:home", TX: boolPtr(false)}
	frame := api.FrameInfo{Source: "fixtures/home.bin", Length: 371}

	snapshot, changed := store.Apply(Update{
		State:     state,
		Telemetry: telemetry,
		Frame:     frame,
		FrameKind: "home",
		Source:    "fixture:home",
	})
	if !changed {
		t.Fatal("expected first apply to change snapshot")
	}
	if snapshot.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", snapshot.Sequence)
	}
	if snapshot.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}

	snapshot, changed = store.Apply(Update{
		State:     state,
		Telemetry: telemetry,
		Frame:     frame,
		FrameKind: "home",
		Source:    "fixture:home",
	})
	if changed {
		t.Fatal("expected identical apply to be ignored")
	}
	if snapshot.Sequence != 1 {
		t.Fatalf("sequence after identical apply = %d, want 1", snapshot.Sequence)
	}
}

func TestStoreApplyTreatsEqualLCDFlagsAsIdentical(t *testing.T) {
	store := NewStore(Snapshot{})
	state := display.DemoState()
	telemetry := api.Telemetry{Source: "serial", TX: boolPtr(false)}
	frame := api.FrameInfo{
		Source:      "serial",
		Length:      447,
		StartOffset: 9,
		LCDFlags: &api.LCDFlags{
			RawInverted:     0xf801,
			Decoded:         0x07fe,
			ChecksumPresent: true,
			ChecksumValid:   true,
			LEDs:            &api.LCDLEDs{TX: false, Operate: false, Set: false, Tune: false},
		},
	}

	snapshot, changed := store.Apply(Update{State: state, Telemetry: telemetry, Frame: frame, FrameKind: "serial", Source: "serial"})
	if !changed {
		t.Fatal("expected first apply to change snapshot")
	}
	if snapshot.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", snapshot.Sequence)
	}

	// Rebuild the same frame with new pointer identities. This mirrors live serial
	// decoding, where LCDFlags/LEDs are freshly allocated on every poll even when
	// the visible display and flag values are unchanged.
	sameFrame := api.FrameInfo{
		Source:      "serial",
		Length:      447,
		StartOffset: 9,
		LCDFlags: &api.LCDFlags{
			RawInverted:     0xf801,
			Decoded:         0x07fe,
			ChecksumPresent: true,
			ChecksumValid:   true,
			LEDs:            &api.LCDLEDs{TX: false, Operate: false, Set: false, Tune: false},
		},
	}
	snapshot, changed = store.Apply(Update{State: state, Telemetry: telemetry, Frame: sameFrame, FrameKind: "serial", Source: "serial"})
	if changed {
		t.Fatal("expected equal LCD flag values with new pointers to be ignored")
	}
	if snapshot.Sequence != 1 {
		t.Fatalf("sequence after equal LCD flag apply = %d, want 1", snapshot.Sequence)
	}
}

func TestStoreApplyFallsBackToTelemetrySource(t *testing.T) {
	store := NewStore(Snapshot{})
	snapshot, changed := store.Apply(Update{
		State:     display.DemoState(),
		Telemetry: api.Telemetry{Source: "fixture:menu"},
		FrameKind: "menu",
	})
	if !changed {
		t.Fatal("expected snapshot update")
	}
	if snapshot.Source != "fixture:menu" {
		t.Fatalf("source = %q, want fixture:menu", snapshot.Source)
	}
}

type sequenceSource struct {
	updates []Update
	index   int
}

func (s *sequenceSource) Poll(context.Context) (Update, error) {
	if s.index >= len(s.updates) {
		return s.updates[len(s.updates)-1], nil
	}
	update := s.updates[s.index]
	s.index++
	return update, nil
}

func TestPollerRunSeedsStoreFromSource(t *testing.T) {
	store := NewStore(Snapshot{})
	source := &sequenceSource{updates: []Update{{
		State:     display.DemoStateAlt(),
		Telemetry: api.Telemetry{Mode: "operate", Source: "fixture:home", TX: boolPtr(false)},
		Frame:     api.FrameInfo{Source: "fixtures/home.bin"},
		FrameKind: "home",
		Source:    "fixture:home",
	}}}
	poller := &Poller{Source: source, Store: store, Interval: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := poller.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	snapshot := store.Current()
	if snapshot.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", snapshot.Sequence)
	}
	if snapshot.Telemetry.Mode != "operate" {
		t.Fatalf("mode = %q, want operate", snapshot.Telemetry.Mode)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestPollerSkipsTickWhenDisabled(t *testing.T) {
	store := NewStore(Snapshot{})

	var pollCount atomic.Int64
	source := &countingSource{onPoll: func() { pollCount.Add(1) }}

	var enabled atomic.Bool
	enabled.Store(false)
	poller := &Poller{
		Source:   source,
		Store:    store,
		Interval: 5 * time.Millisecond,
		Enabled:  enabled.Load,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	// Let a few ticks pass while disabled.
	time.Sleep(30 * time.Millisecond)
	if got := pollCount.Load(); got != 0 {
		cancel()
		t.Fatalf("expected 0 polls while disabled, got %d", got)
	}

	// Enable and let ticks run.
	enabled.Store(true)
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	if got := pollCount.Load(); got == 0 {
		t.Fatal("expected at least one poll after enabling, got 0")
	}
}

func TestStoreSubscribeReceivesLatestSnapshotUpdates(t *testing.T) {
	store := NewStore(Snapshot{Sequence: 4, Source: "fixture:home"})
	updates, unsubscribe := store.Subscribe(1)
	defer unsubscribe()

	snapshot, changed := store.Apply(Update{Source: "serial", Telemetry: api.Telemetry{Source: "serial"}})
	if !changed {
		t.Fatal("expected store apply to change snapshot")
	}

	select {
	case got := <-updates:
		if got.Sequence != snapshot.Sequence || got.Source != "serial" {
			t.Fatalf("unexpected subscribed snapshot: %+v want %+v", got, snapshot)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for subscribed snapshot")
	}
}

type countingSource struct {
	onPoll func()
}

func (s *countingSource) Poll(_ context.Context) (Update, error) {
	if s.onPoll != nil {
		s.onPoll()
	}
	return Update{}, nil
}
