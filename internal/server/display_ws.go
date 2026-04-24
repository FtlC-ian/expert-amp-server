package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/gorilla/websocket"
)

type displayRenderEvent struct {
	// Sequence is the authoritative runtime snapshot sequence for display invalidation.
	// A zero value is valid for an empty initial snapshot and should not trigger a refresh.
	Sequence  uint64    `json:"sequence"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
	Source    string    `json:"source,omitempty"`
	FrameKind string    `json:"frameKind,omitempty"`
}

func handleDisplayWebsocket(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowMethodAPI(w, r, http.MethodGet) {
			return
		}
		if opts.Store == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "runtime snapshot unavailable")
			return
		}

		conn, err := statusWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Time{})
		conn.SetPongHandler(func(string) error { return nil })

		send := func(snapshot runtime.Snapshot) error {
			event := displayRenderEvent{
				Sequence:  snapshot.Sequence,
				UpdatedAt: snapshot.UpdatedAt,
				Source:    snapshot.Source,
				FrameKind: snapshot.FrameKind,
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			return conn.WriteMessage(websocket.TextMessage, payload)
		}

		current := currentSnapshot(opts.Store)
		if err := send(current); err != nil {
			return
		}

		updates, unsubscribe := opts.Store.Subscribe(2)
		defer unsubscribe()

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-pingTicker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case snapshot, ok := <-updates:
				if !ok {
					return
				}
				if err := send(snapshot); err != nil {
					return
				}
			}
		}
	}
}
