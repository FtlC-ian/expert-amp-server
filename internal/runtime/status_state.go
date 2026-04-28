package runtime

import (
	"reflect"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

// StatusState keeps the latest protocol-native status frame separately from the
// display/render/runtime snapshot. Call Resolve with the current runtime
// snapshot to get the canonical API status view.
type StatusState struct {
	mu             sync.RWMutex
	protocolNative api.Status
	lastProtocolAt time.Time
	subscribers    map[chan api.Status]struct{}
}

const RecentContactWindow = 5 * time.Second

func NewStatusState(initial api.Status) *StatusState {
	return &StatusState{
		protocolNative: initial,
		subscribers:    make(map[chan api.Status]struct{}),
	}
}

func (s *StatusState) CurrentProtocolNative() api.Status {
	if s == nil {
		return api.Status{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.protocolNative
}

func (s *StatusState) UpdateProtocolNative(status api.Status) {
	if s == nil {
		return
	}

	now := time.Now().UTC()
	s.mu.Lock()
	changed := !reflect.DeepEqual(s.protocolNative, status)
	s.protocolNative = status
	s.lastProtocolAt = now
	subscribers := make([]chan api.Status, 0, len(s.subscribers))
	if changed {
		for ch := range s.subscribers {
			subscribers = append(subscribers, ch)
		}
	}
	s.mu.Unlock()

	if !changed {
		return
	}
	for _, ch := range subscribers {
		pushStatus(ch, status)
	}
}

func (s *StatusState) Subscribe(buffer int) (<-chan api.Status, func()) {
	if s == nil {
		return nil, func() {}
	}
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan api.Status, buffer)

	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = make(map[chan api.Status]struct{})
	}
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[ch]; !ok {
			return
		}
		delete(s.subscribers, ch)
		close(ch)
	}

	return ch, unsubscribe
}

func (s *StatusState) Resolve(snapshot Snapshot) api.Status {
	fallback := StatusFromSnapshot(snapshot)
	if s == nil {
		return applyContactMetadata(fallback, snapshot.UpdatedAt, time.Time{})
	}
	status := s.CurrentProtocolNative()
	protocolAt := s.protocolUpdatedAt()
	if status.Provenance != "status-poll" {
		return applyContactMetadata(fallback, snapshot.UpdatedAt, protocolAt)
	}
	resolved := mergeProtocolNativeStatus(status, fallback)
	resolved = applyFreshDisplayOverrides(resolved, fallback, status, snapshot.UpdatedAt, protocolAt)
	return applyContactMetadata(resolved, snapshot.UpdatedAt, protocolAt)
}

func (s *StatusState) protocolUpdatedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastProtocolAt
}

func applyContactMetadata(status api.Status, snapshotAt, protocolAt time.Time) api.Status {
	last := snapshotAt
	if protocolAt.After(last) {
		last = protocolAt
	}
	if last.IsZero() {
		status.RecentContact = false
		status.LastContactAt = ""
		return status
	}
	status.LastContactAt = last.UTC().Format(time.RFC3339)
	status.RecentContact = time.Since(last) <= RecentContactWindow
	return status
}

func pushStatus(ch chan api.Status, status api.Status) {
	select {
	case ch <- status:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- status:
	default:
	}
}

func mergeProtocolNativeStatus(protocol api.Status, fallback api.Status) api.Status {
	merged := protocol
	mergeZeroStatusFields(reflect.ValueOf(&merged).Elem(), reflect.ValueOf(fallback))
	if protocol.Band == "" && protocol.BandText != "" {
		merged.Band = ""
	}
	return merged
}

func applyFreshDisplayOverrides(resolved api.Status, fallback api.Status, protocol api.Status, snapshotAt, protocolAt time.Time) api.Status {
	if snapshotAt.IsZero() || protocolAt.IsZero() || !snapshotAt.After(protocolAt) {
		return resolved
	}
	// Operating state is operator-visible display state that can lag in the
	// protocol-native status poll immediately after a front-panel/button action.
	// Only override when the display snapshot is strictly newer, and only when
	// the display-derived fallback actually has a meaningful value.
	if isCanonicalOperatingState(fallback.OperatingState) {
		resolved.OperatingState = fallback.OperatingState
	}
	if isCanonicalOperatingState(fallback.Mode) {
		resolved.Mode = fallback.Mode
	}
	// Keep protocol-native outputLevel authoritative when the status poll reports
	// it. Unlike operate/standby text, the documented status poll has a direct
	// power-level field; overriding it with fresher display text lets transient
	// menu/button echo frames wobble canonical status through LOW/MID/HIGH/MAX.
	if protocol.OutputLevel == "" && fallback.OutputLevel != "" {
		resolved.OutputLevel = fallback.OutputLevel
	}
	return resolved
}

func isCanonicalOperatingState(value string) bool {
	switch value {
	case "standby", "operate":
		return true
	default:
		return false
	}
}

func mergeZeroStatusFields(dst reflect.Value, fallback reflect.Value) {
	for i := range dst.NumField() {
		dstField := dst.Field(i)
		fallbackField := fallback.Field(i)
		if dst.Type().Field(i).Anonymous {
			mergeZeroStatusFields(dstField, fallbackField)
			continue
		}
		if !dstField.CanSet() || !statusFieldNeedsFallback(dstField) {
			continue
		}
		dstField.Set(fallbackField)
	}
}

func statusFieldNeedsFallback(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return v.Len() == 0
	case reflect.Struct:
		return v.IsZero()
	default:
		return v.IsZero()
	}
}

func StatusFromSnapshot(snapshot Snapshot) api.Status {
	return api.Status{Telemetry: snapshot.Telemetry}
}
