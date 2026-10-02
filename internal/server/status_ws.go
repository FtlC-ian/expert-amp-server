package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/gorilla/websocket"
)

var statusWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// statusRecheckInterval bounds how long an open socket can serve a status view
// that has expired on a timer rather than by anything being published.
//
// Not every change to canonical status arrives as an event. Tapped status stops
// being authoritative once it is older than runtime.RecentContactWindow, and
// recentContact ages out the same way: both are decided inside Resolve, from the
// clock, so the moment they flip nobody calls UpdateProtocolNative and nobody
// calls InvalidatePreLeaseStatus. A socket waiting only on subscriptions never
// learns. That is not a corner case -- a lease whose client stops polling 0x90
// against a static amplifier screen publishes nothing at all, which is the
// measured behavior of a real Expert Controller Plus session.
//
// One second matches the cadence safetyContactLoop already uses to notice the
// same staleness, and bounds the lag to about a second past the window.
// Re-resolving costs nothing visible: sendIfChanged compares against the last
// payload and writes only on a difference, so a steady socket stays silent.
const statusRecheckInterval = time.Second

func handleStatusWebsocket(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}

		conn, err := statusWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Time{})
		conn.SetPongHandler(func(string) error { return nil })

		// Subscribe before resolving the first payload, never after. Authority
		// can change between the two -- a lease invalidating the retained frame,
		// or the first poll after one restoring it -- and a transition published
		// into that gap reaches nobody, because this connection is not a
		// subscriber yet. The socket then waits for the next wake-up that may
		// never come: a static display publishes nothing, and a steady-state
		// amplifier repeats its status bytes, so it can serve the wrong
		// authority for the life of the connection.
		//
		// Subscribing first cannot lose it. The channels are buffered and
		// pushStatus overwrites rather than drops, so a transition that lands
		// before the initial Resolve stays queued and the loop immediately
		// rechecks; if that Resolve already saw the new state, sendIfChanged
		// finds nothing to send. Either order of arrival ends up correct.
		statusUpdates, unsubscribeStatus := subscribeStatus(opts)
		defer unsubscribeStatus()
		snapshotUpdates, unsubscribeSnapshots := subscribeSnapshots(opts.Store)
		defer unsubscribeSnapshots()

		status := selectedStatus(opts)
		if err := writeStatusWebsocketMessage(conn, status); err != nil {
			return
		}
		last := status

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()

		// See statusRecheckInterval: the ping ticker deliberately does not
		// re-resolve, so without this the only things that can move this socket
		// are published events -- and a status view that expires on the clock
		// publishes none.
		recheckTicker := time.NewTicker(statusRecheckInterval)
		defer recheckTicker.Stop()

		sendIfChanged := func() error {
			status := selectedStatus(opts)
			if reflect.DeepEqual(status, last) {
				return nil
			}
			if err := writeStatusWebsocketMessage(conn, status); err != nil {
				return err
			}
			last = status
			return nil
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case <-pingTicker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-recheckTicker.C:
				if err := sendIfChanged(); err != nil {
					return
				}
			case _, ok := <-statusUpdates:
				if !ok {
					statusUpdates = nil
					continue
				}
				if err := sendIfChanged(); err != nil {
					return
				}
			case _, ok := <-snapshotUpdates:
				if !ok {
					snapshotUpdates = nil
					continue
				}
				if err := sendIfChanged(); err != nil {
					return
				}
			}
		}
	}
}

func subscribeStatus(opts Options) (<-chan api.Status, func()) {
	if opts.StatusState == nil {
		return nil, func() {}
	}
	return opts.StatusState.Subscribe(2)
}

func subscribeSnapshots(store *runtime.Store) (<-chan runtime.Snapshot, func()) {
	if store == nil {
		return nil, func() {}
	}
	return store.Subscribe(2)
}

func writeStatusWebsocketMessage(conn *websocket.Conn, status api.Status) error {
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, payload)
}
