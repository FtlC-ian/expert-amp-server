package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
)

func TestRepeatedDisplayArrivalRefreshesContactWithoutChangingSnapshot(t *testing.T) {
	frame, err := protocol.ReadFixtureBytes("../../fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Fatal(err)
	}
	source := NewSerialSource(SerialSourceConfig{}, nil, Update{})
	source.applyFrame(frame)
	update, _ := source.Poll(context.Background())
	store := NewStore(Snapshot{})
	first, _ := store.Apply(update)
	first.UpdatedAt = time.Now().Add(-2 * RecentContactWindow)
	store = NewStore(first)
	changes, unsubscribe := store.Subscribe(1)
	defer unsubscribe()
	source.applyFrame(frame)
	update, _ = source.Poll(context.Background())
	current, changed := store.Apply(update)
	if changed || current.Sequence != first.Sequence || current.UpdatedAt != first.UpdatedAt {
		t.Fatalf("identical arrival changed display identity: %+v", current)
	}
	select {
	case <-changes:
		t.Fatal("contact-only arrival published a display change")
	default:
	}
	status := source.StatusState().Resolve(current)
	if !status.RecentContact || status.LastContactAt == "" {
		t.Fatalf("identical live display arrival did not refresh contact: %+v", status)
	}
	if status.LastContactAt != current.DisplayReceivedAt.UTC().Format(time.RFC3339) {
		t.Fatal("canonical contact did not use the accepted arrival")
	}
	if source.StatusState().CurrentProtocolNativeWithContact().RecentContact {
		t.Fatal("display contact granted direct status-poll freshness")
	}
	if !current.DisplayReceivedAt.After(first.DisplayReceivedAt) {
		t.Fatal("identical arrival did not advance its own clock")
	}
	for _, ingest := range []struct {
		name string
		run  func()
	}{
		{"cached poll", func() {}},
		{"malformed frame", func() { source.applyFrame([]byte("not a display frame")) }},
		{"retired session", func() { source.applyFrameFromSession(frame, 99) }},
	} {
		t.Run(ingest.name, func(t *testing.T) {
			ingest.run()
			cached, _ := source.Poll(context.Background())
			got, changed := store.Apply(cached)
			if changed || !got.DisplayReceivedAt.Equal(current.DisplayReceivedAt) {
				t.Fatal("non-arrival advanced display/contact state")
			}
		})
	}
	time.Sleep(RecentContactWindow + 100*time.Millisecond)
	update, _ = source.Poll(context.Background())
	current, _ = store.Apply(update)
	if source.StatusState().Resolve(current).RecentContact {
		t.Fatal("cached polling kept a silent display link fresh")
	}
}

func TestDisplayContactDoesNotRenewProtocolOrSessionAuthority(t *testing.T) {
	frame, err := protocol.ReadFixtureBytes("../../fixtures/real_home_status_frame.bin")
	if err != nil {
		t.Fatal(err)
	}
	source := NewSerialSource(SerialSourceConfig{}, nil, Update{})
	firstGeneration, _ := source.beginSession(&mockSerialPort{})
	source.applyFrameFromSession(frame, firstGeneration)
	first, _ := source.Poll(context.Background())
	secondGeneration, _ := source.beginSession(&mockSerialPort{})
	source.applyFrameFromSession(frame, firstGeneration)
	retained, _ := source.Poll(context.Background())
	if !retained.DisplayReceivedAt.Equal(first.DisplayReceivedAt) {
		t.Fatal("retired session renewed display contact")
	}
	source.statusState.UpdateProtocolNative(api.Status{Telemetry: api.Telemetry{Provenance: "status-poll"}})
	source.statusState.mu.Lock()
	source.statusState.lastProtocolAt = time.Now().Add(-2 * RecentContactWindow)
	source.statusState.mu.Unlock()
	source.applyFrameFromSession(frame, secondGeneration)
	update, _ := source.Poll(context.Background())
	current, _ := NewStore(Snapshot{}).Apply(update)
	if !source.StatusState().Resolve(current).RecentContact {
		t.Fatal("replacement-session display arrival did not report link contact")
	}
	if source.StatusState().CurrentProtocolNativeWithContact().RecentContact {
		t.Fatal("fresh display renewed stale direct status-poll evidence")
	}
	if evidence := source.SerialSessionEvidence(); evidence.StatusGeneration != 0 {
		t.Fatal("display contact granted replacement-session status authority")
	}
}

func TestStoreContactClockDoesNotRegressOrBorrowChangeClock(t *testing.T) {
	arrival := time.Now().Add(-2 * RecentContactWindow)
	store := NewStore(Snapshot{DisplayReceivedAt: arrival})
	for _, timestamp := range []time.Time{arrival.Add(-time.Second), {}} {
		current, _ := store.Apply(Update{Source: "fixture", DisplayReceivedAt: timestamp})
		if !current.DisplayReceivedAt.Equal(arrival) {
			t.Fatal("older or absent arrival regressed contact clock")
		}
		if NewStatusState(api.Status{}).Resolve(current).RecentContact {
			t.Fatal("content change refreshed stale arrival contact")
		}
	}
	current, _ := NewStore(Snapshot{}).Apply(Update{Source: "fixture"})
	var state *StatusState
	if status := state.Resolve(current); status.RecentContact || status.LastContactAt != "" {
		t.Fatal("fixture/change-only snapshot invented contact")
	}
}
