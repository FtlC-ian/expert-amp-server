package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
)

func TestV1StatusWebsocketTracksContactWithoutDisplayChanges(t *testing.T) {
	initial := runtime.Snapshot{
		Telemetry: api.Telemetry{Band: "20m", Source: "serial", Provenance: "display-frame"},
		Source:    "serial", Sequence: 7, UpdatedAt: time.Now().Add(-time.Hour),
		DisplayReceivedAt: time.Now().Add(-2 * runtime.RecentContactWindow),
	}
	store := runtime.NewStore(initial)
	changes, unsubscribe := store.Subscribe(1)
	defer unsubscribe()
	server := httptest.NewServer(newTestHandler(store, runtime.FixtureCatalog{}))
	defer server.Close()
	conn := dialWS(t, server.URL, "/api/v1/status/ws")
	defer conn.Close()
	first := readStatusWSMessage(t, conn)
	if first.RecentContact || first.LastContactAt == "" {
		t.Fatalf("initial silent link must be stale: %+v", first)
	}

	arrival := time.Now().UTC()
	update := runtime.Update{Telemetry: initial.Telemetry, DisplayReceivedAt: arrival}
	current, changed := store.Apply(update)
	if changed || current.Sequence != initial.Sequence || !current.UpdatedAt.Equal(initial.UpdatedAt) {
		t.Fatal("contact-only arrival changed display identity")
	}
	resumed := readStatusWSMessage(t, conn)
	want := first
	want.RecentContact = true
	want.LastContactAt = arrival.Format(time.RFC3339)
	if !reflect.DeepEqual(resumed, want) {
		t.Fatalf("unchanged display did not restore websocket contact: got %+v, want %+v", resumed, want)
	}

	// Cached polls must neither renew contact nor produce duplicate status frames.
	for range 3 {
		store.Apply(update)
	}
	if err := conn.SetReadDeadline(arrival.Add(runtime.RecentContactWindow + 3*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("silent link never expired on the websocket: %v", err)
	}
	var expired api.Status
	if err := json.Unmarshal(payload, &expired); err != nil {
		t.Fatal(err)
	}
	want.RecentContact = false
	if !reflect.DeepEqual(expired, want) {
		t.Fatalf("next message must be silence expiry, not duplicate/contact renewal: got %+v, want %+v", expired, want)
	}
	if time.Since(arrival) <= runtime.RecentContactWindow {
		t.Fatal("contact expired before the canonical window")
	}
	select {
	case <-changes:
		t.Fatal("status freshness published a display/content event")
	default:
	}

	// At least two further rechecks must remain silent after expiry.
	if err := conn.SetReadDeadline(time.Now().Add(2200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("unchanged expired status was published again")
	} else if timeout, ok := err.(interface{ Timeout() bool }); !ok || !timeout.Timeout() {
		t.Fatalf("expected a quiet open socket, got %v", err)
	}
}

func TestV1StatusWebsocketContactRechecksRespectCancellation(t *testing.T) {
	initial := runtime.Snapshot{Source: "serial", DisplayReceivedAt: time.Now().UTC()}
	store := runtime.NewStore(initial)
	handler := newTestHandler(store, runtime.FixtureCatalog{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	conn := dialWS(t, server.URL, "/api/v1/status/ws")
	defer conn.Close()
	if !readStatusWSMessage(t, conn).RecentContact {
		t.Fatal("fresh connection did not report contact")
	}
	applying := make(chan struct{})
	go func() {
		defer close(applying)
		for range 1000 {
			store.Apply(runtime.Update{Source: "serial", DisplayReceivedAt: time.Now().UTC()})
		}
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("status websocket handler outlived request cancellation")
	}
	<-applying
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("canceled socket sent an extra status message")
	}
}
