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

		status := selectedStatus(opts)
		if err := writeStatusWebsocketMessage(conn, status); err != nil {
			return
		}
		last := status

		statusUpdates, unsubscribeStatus := subscribeStatus(opts)
		defer unsubscribeStatus()
		snapshotUpdates, unsubscribeSnapshots := subscribeSnapshots(opts.Store)
		defer unsubscribeSnapshots()

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()

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
