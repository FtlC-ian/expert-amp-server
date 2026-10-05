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

	// protocolGeneration counts published protocol-native frames. It starts at 1
	// so the seed status counts as a generation and the zero value of
	// invalidBeforeGeneration means "nothing has been invalidated yet".
	protocolGeneration      uint64
	invalidBeforeGeneration uint64

	subscribers map[chan api.Status]struct{}
}

const RecentContactWindow = 5 * time.Second

func NewStatusState(initial api.Status) *StatusState {
	return &StatusState{
		protocolNative:     initial,
		protocolGeneration: 1,
		subscribers:        make(map[chan api.Status]struct{}),
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

// CurrentProtocolNativeWithContact returns only the protocol-native snapshot
// with freshness recalculated at read time. It intentionally does not merge
// display-derived fallback fields.
func (s *StatusState) CurrentProtocolNativeWithContact() api.Status {
	if s == nil {
		return api.Status{}
	}
	s.mu.RLock()
	status := s.protocolNative
	protocolAt := s.lastProtocolAt
	s.mu.RUnlock()
	return applyContactMetadata(status, time.Time{}, protocolAt)
}

func (s *StatusState) UpdateProtocolNative(status api.Status) {
	if s == nil {
		return
	}

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := !reflect.DeepEqual(s.protocolNative, status)
	wasAuthoritative := s.authoritativeLocked()
	s.protocolNative = status
	s.lastProtocolAt = now
	// Counts every publication, not only changed ones: an unchanged repeat is
	// still fresh evidence, and is what lifts a passthrough invalidation.
	s.protocolGeneration++
	// A frame that lifts an invalidation changes what Resolve answers even when
	// the bytes are identical -- a steady-state amplifier repeats itself, so the
	// first poll after a lease is routinely byte-for-byte the pre-lease reply.
	// Notifying only on changed bytes would restore direct-GET semantics while
	// leaving every open status websocket on the display-derived fallback.
	if changed || s.authoritativeLocked() != wasAuthoritative {
		s.notifySubscribersLocked(status)
	}
}

// Subscribe returns a channel that wakes when the canonical status view may have
// changed. The delivered api.Status is the retained protocol-native frame, which
// is deliberately not the answer: a subscriber that needs canonical status must
// call Resolve for itself, because a wake-up can mean the retained frame stopped
// being authoritative rather than that its contents moved. Sends are dropped
// rather than queued, so a slow subscriber costs the publisher nothing.
//
// The returned function removes the subscriber and closes its channel, so a
// reader may observe the close; it is idempotent, and it is serialized against
// delivery by mu, so it can never close a channel a publisher is about to send
// on. See notifySubscribersLocked.
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

// InvalidatePreLeaseStatus marks the retained protocol-native status as no
// longer canonical. Raw passthrough calls it as a lease begins, because the
// lease stops the server's own polling: without it the last pre-lease
// status-poll frame keeps both its "status-poll" label and its freshness
// window, so /api/v1/status and /api/v1/alarms would report protocol-only
// fields -- temperature, SWR, TX, output level -- as current on behalf of a
// client that may never ask the amplifier for status at all.
//
// It invalidates rather than clears, so nothing has to be restored: the next
// published frame, whether a tapped 0x90 during the lease or the first real
// poll after it, makes canonical status authoritative again on its own.
//
// Losing authority changes what Resolve answers without changing a byte of the
// retained frame, so subscribers are woken here too. Without that an already-open
// status websocket keeps serving the pre-lease status-poll payload for as long as
// the external client holds the port and the decoded display stays still.
func (s *StatusState) InvalidatePreLeaseStatus() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Invalidating what is already invalid changes nothing, so it wakes nobody.
	// The post-state is non-authoritative by construction, which is why only the
	// prior value has to be tested.
	notify := s.authoritativeLocked()
	s.invalidBeforeGeneration = s.protocolGeneration
	if notify {
		s.notifySubscribersLocked(s.protocolNative)
	}
}

// authoritativeLocked reports whether the retained protocol-native frame still
// speaks for the amplifier. Callers must hold mu, for read or write.
func (s *StatusState) authoritativeLocked() bool {
	return s.protocolGeneration > s.invalidBeforeGeneration
}

// notifySubscribersLocked wakes every current subscriber. Callers must hold mu
// for write, and delivery deliberately happens under that lock rather than to a
// copied set after unlocking: unsubscribe closes its channel while holding mu,
// so any copy-then-unlock-then-send publisher can be overtaken by a disconnect
// and send on a closed channel, which panics. Holding mu across the send is what
// makes removal and delivery mutually exclusive.
//
// It stays cheap enough to do under the lock because pushStatus never blocks --
// a full subscriber is drained and overwritten, never waited on. That is also
// what keeps it safe to call from BeginRawPassthrough while it holds
// lifecycleMu: the publisher acquires no further lock and never waits on a
// subscriber, and a woken subscriber wants only this mutex, which it gets as
// soon as the publisher returns.
func (s *StatusState) notifySubscribersLocked(status api.Status) {
	for ch := range s.subscribers {
		pushStatus(ch, status)
	}
}

// protocolSnapshot reads the retained status with the metadata Resolve needs to
// judge it, under one lock so the three cannot disagree with each other.
func (s *StatusState) protocolSnapshot() (api.Status, time.Time, bool) {
	if s == nil {
		return api.Status{}, time.Time{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.protocolNative, s.lastProtocolAt, s.authoritativeLocked()
}

func (s *StatusState) Resolve(snapshot Snapshot) api.Status {
	fallback := StatusFromSnapshot(snapshot)
	if s == nil {
		return applyContactMetadata(fallback, snapshot.UpdatedAt, time.Time{})
	}
	status, protocolAt, authoritative := s.protocolSnapshot()
	// Report display-derived state alone while the retained frame is
	// invalidated. Its timestamp is deliberately dropped as well: a lease can
	// begin within the contact window of the last poll, so passing it here
	// would answer recentContact true for a reading nothing is refreshing.
	if !authoritative {
		return applyContactMetadata(fallback, snapshot.UpdatedAt, time.Time{})
	}
	// A tapped frame speaks for the amplifier only while the external client
	// keeps asking for one, and nothing obliges it to. Expert Controller Plus
	// never sends 0x90 at all, so a lease can produce one tapped frame -- or
	// none -- and then nothing for its entire life while display frames keep
	// arriving at full rate.
	//
	// Without this the retained tapped frame stays canonical forever: merging
	// gives its nonzero fields precedence over the newer display telemetry, so
	// a stale tapped temperature outranks a fresh display-derived one, and
	// applyContactMetadata then reports recentContact from the newer display
	// timestamp -- freshness borrowed from evidence that did not supply the
	// reading. Expiring it back to display-derived state is the honest answer:
	// the display is what is still being refreshed, so let it speak for itself.
	//
	// Only tapped provenance expires here. A status-poll frame is refreshed by
	// the server's own polling, whose age recentContact already reports
	// truthfully; it has no second source racing ahead of it the way a lease
	// puts fresh display frames alongside a frozen tap.
	if status.Provenance == ProvenancePassthroughTap && !tapStillSpeaks(protocolAt) {
		return applyContactMetadata(fallback, snapshot.UpdatedAt, time.Time{})
	}
	// Resolve is the display path, so it merges tapped state too. The
	// provenance travels with the merged status, so callers that need
	// authority -- fan policy, overtemperature standby, menu debug -- still
	// see "passthrough-tap" and refuse it. Only the display is widened here.
	if status.Provenance != "status-poll" && status.Provenance != ProvenancePassthroughTap {
		return applyContactMetadata(fallback, snapshot.UpdatedAt, protocolAt)
	}
	resolved := mergeProtocolNativeStatus(status, fallback)
	resolved = applyFreshDisplayOverrides(resolved, fallback, status, snapshot.UpdatedAt, protocolAt)
	return applyContactMetadata(resolved, snapshot.UpdatedAt, protocolAt)
}

// tapStillSpeaks reports whether a passthrough-tapped status frame is recent
// enough to stand as canonical status. It uses the same window as
// RawPassthroughHandle.TapIsFresh, which is what /api/v1/raw-passthrough
// already reports as tapFresh, so the endpoint and canonical status cannot
// disagree about whether the tap is alive.
func tapStillSpeaks(protocolAt time.Time) bool {
	return !protocolAt.IsZero() && time.Since(protocolAt) <= RecentContactWindow
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
