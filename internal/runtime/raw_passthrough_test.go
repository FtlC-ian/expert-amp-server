package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/fanpolicy"
	"github.com/FtlC-ian/expert-amp-server/internal/menudebug"
	"github.com/FtlC-ian/expert-amp-server/internal/monitoring"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

func newPassthroughTestSource(t *testing.T, opener serial.PortOpener) *SerialSource {
	t.Helper()
	return newPassthroughTestSourceWithPolling(t, opener, "both", true)
}

func newPassthroughTestSourceWithPolling(t *testing.T, opener serial.PortOpener, mode string, statusPoll bool) *SerialSource {
	t.Helper()
	return NewSerialSource(SerialSourceConfig{
		Port:                     "/dev/ttyTEST0",
		BaudRate:                 115200,
		ReadTimeout:              10 * time.Millisecond,
		ReadSize:                 512,
		MinFrameLen:              64,
		MaxBuffer:                8192,
		IOTimeout:                2 * time.Second,
		ReconnectBackoff:         100 * time.Millisecond,
		PollingMode:              mode,
		StatusPollCommandEnabled: statusPoll,
	}, opener, Update{})
}

// The live read loop must be fully stopped, not merely told to stop, before the
// caller is handed the port. Mirrors the wake test's guarantee.
func TestBeginRawPassthroughQuiescesLiveSessionAndReconnectsOnClose(t *testing.T) {
	live := &mockSerialPort{blockRead: true}
	leased := &mockSerialPort{blockRead: true}
	reconnected := &mockSerialPort{chunks: [][]byte{displayStreamChunk(t)}}
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{live, leased, reconnected},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	beginCtx, beginCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer beginCancel()
	handle, err := src.BeginRawPassthrough(beginCtx)
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	if !live.isClosed() {
		t.Fatal("passthrough did not retire the live reader before leasing the port")
	}
	if handle.Port() != serial.Port(leased) {
		t.Fatal("passthrough did not return the newly opened port")
	}

	handle.Close()

	waitForCondition(t, 2*time.Second, func() bool {
		diag := src.Diagnostics()
		return opener.openCount() >= 3 && diag.Connected
	})
	if !leased.isClosed() {
		t.Fatal("closing the handle did not close the leased port")
	}

	// Close is idempotent: a second call must not unlock lifecycleMu twice.
	handle.Close()
}

// Regression test for the deadlock this feature's first design would have
// caused. SendWake takes writeMu and then lifecycleMu, and it is called with
// the actuation coordinator's mutex held, so blocking it on the lifecycleMu a
// passthrough session holds for its whole duration would wedge every actuation
// path until the external client disconnected. Both calls must fail fast.
func TestRawPassthroughRefusesServerWritesWithoutBlocking(t *testing.T) {
	live := &mockSerialPort{blockRead: true}
	leased := &mockSerialPort{blockRead: true}
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{live, leased, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer handle.Close()

	type outcome struct {
		err error
	}

	wakeDone := make(chan outcome, 1)
	go func() {
		_, err := src.SendWake(context.Background())
		wakeDone <- outcome{err: err}
	}()

	buttonDone := make(chan outcome, 1)
	go func() {
		_, err := src.SendButton(context.Background(), api.ButtonAction{Name: "operate"})
		buttonDone <- outcome{err: err}
	}()

	for _, tc := range []struct {
		name string
		ch   chan outcome
	}{
		{"SendWake", wakeDone},
		{"SendButton", buttonDone},
	} {
		select {
		case got := <-tc.ch:
			if got.err == nil {
				t.Fatalf("%s: expected refusal while passthrough holds the port", tc.name)
			}
			if transport.ButtonStatusCode(got.err) != 409 {
				t.Fatalf("%s: expected HTTP 409, got %d (%v)", tc.name, transport.ButtonStatusCode(got.err), got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s blocked while a raw passthrough session held the port", tc.name)
		}
	}
}

// A second concurrent client is rejected outright rather than queued, matching
// the actuation coordinator's reject-don't-queue rule.
func TestBeginRawPassthroughRejectsSecondClient(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	first, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("first BeginRawPassthrough error: %v", err)
	}
	defer first.Close()

	second, err := src.BeginRawPassthrough(context.Background())
	if !errors.Is(err, ErrRawPassthroughBusy) {
		t.Fatalf("expected ErrRawPassthroughBusy, got %v", err)
	}
	if second != nil {
		t.Fatal("expected no handle for the rejected second client")
	}
}

// A tapped frame is display evidence only. It must be labelled passthrough-tap,
// must never reach the safety controller, and must never end the session -- the
// operator keeps their live control link, and server-side automatic control was
// already refused before the lease started.
func TestRawPassthroughTapIsDisplayOnlyAndNeverActuates(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	safety := monitoring.NewController(nil)
	src.ConfigureSafetyController(safety, func() monitoring.ControlSettings {
		return monitoring.ControlSettings{
			Enabled:    true,
			Armed:      true,
			Thresholds: monitoring.Thresholds{TemperatureTripC: 50, TemperatureWarningC: 40, TemperatureResetC: 40},
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer handle.Close()

	// Well past the 50C trip threshold: under the old tap-and-drop behavior
	// this ended the session.
	handle.ObserveFromAmp(statusFrameAtTemperature(t, 80))

	if safety.Apply(monitoring.Result{}, true).Latched {
		t.Fatal("tapped frames must not reach the safety controller")
	}

	tapped := src.statusState.CurrentProtocolNative()
	if tapped.Provenance != ProvenancePassthroughTap {
		t.Fatalf("tapped status provenance = %q, want %q", tapped.Provenance, ProvenancePassthroughTap)
	}
	if !handle.TapIsFresh() {
		t.Fatal("tap should report fresh after observing a status frame")
	}

	// The session is still the operator's; nothing ended it.
	if _, err := src.BeginRawPassthrough(context.Background()); !errors.Is(err, ErrRawPassthroughBusy) {
		t.Fatalf("session should still be held after a hot tapped frame, got %v", err)
	}
}

// The boundary this feature turns on: tapped status is visible to the display
// path but is refused by every automatic actuation gate. Both gates test the
// provenance string exactly, so this pins the string as much as the behavior.
func TestPassthroughTapVisibleForDisplayRejectedForActuation(t *testing.T) {
	hot := 80.0
	tapped := api.Status{
		Telemetry: api.Telemetry{
			Provenance:     ProvenancePassthroughTap,
			OperatingState: "operate",
			TX:             new(bool),
			TemperatureC:   &hot,
			Source:         "serial",
		},
		RecentContact: true,
	}

	// Rejected as authority: overtemperature standby must not latch on it.
	safety := monitoring.NewController(nil)
	safety.Observe(context.Background(), tapped, monitoring.ControlSettings{
		Enabled:    true,
		Armed:      true,
		Thresholds: monitoring.Thresholds{TemperatureTripC: 50, TemperatureWarningC: 40, TemperatureResetC: 40},
	})
	if safety.Apply(monitoring.Result{}, true).Latched {
		t.Fatal("overtemperature standby latched on passthrough-tap status")
	}

	// Rejected as authority: fan policy must report it as unavailable.
	fanResult := fanpolicy.Evaluate(tapped, fanpolicy.Settings{
		Enabled:            true,
		HighTemperatureC:   60,
		NormalTemperatureC: 40,
	}, fanpolicy.PolicyUnknown)
	if fanResult.State != fanpolicy.StateUnavailable {
		t.Fatalf("fan policy state = %q, want %q for passthrough-tap status", fanResult.State, fanpolicy.StateUnavailable)
	}

	// Visible for display: Resolve merges it and keeps the label attached, so a
	// consumer can always tell what it is looking at.
	state := NewStatusState(api.Status{})
	state.UpdateProtocolNative(tapped)
	resolved := state.Resolve(Snapshot{})
	if resolved.Provenance != ProvenancePassthroughTap {
		t.Fatalf("resolved provenance = %q, want the tap label to survive the merge", resolved.Provenance)
	}
	if resolved.TemperatureC == nil || *resolved.TemperatureC != hot {
		t.Fatalf("tapped temperature was not merged for display: %+v", resolved.TemperatureC)
	}
}

// statusFrameAtTemperature rewrites the documented status fixture to report
// temp on all three sensors in OPERATE/RX, keeping every field width identical
// so only the trailing checksum needs recomputing.
func statusFrameAtTemperature(t *testing.T, temp int) []byte {
	t.Helper()
	frame, err := os.ReadFile("../protocol/testdata/status_response_example.bin")
	if err != nil {
		t.Fatalf("read status fixture: %v", err)
	}
	if len(frame) != 75 {
		t.Fatalf("unexpected status fixture length %d", len(frame))
	}
	// Data is frame[4:71] (statusDataLen 67) with the checksum pair right after.
	const dataStart, dataEnd = 4, 71

	fields := strings.Split(string(frame[dataStart:dataEnd]), ",")
	if len(fields) != 19 {
		t.Fatalf("unexpected status fixture field count %d", len(fields))
	}
	fields[1] = "O" // OPERATE
	fields[2] = "R" // RX
	value := fmt.Sprintf("%03d", temp)
	if len(value) != 3 {
		t.Fatalf("temperature %d does not fit the fixture's 3-character field", temp)
	}
	fields[14], fields[15], fields[16] = value, value, value

	data := strings.Join(fields, ",")
	if len(data) != dataEnd-dataStart {
		t.Fatalf("rewritten status data changed length: got %d want %d", len(data), dataEnd-dataStart)
	}

	out := append([]byte(nil), frame...)
	copy(out[dataStart:dataEnd], data)
	sum := 0
	for _, b := range out[dataStart:dataEnd] {
		sum += int(b)
	}
	out[dataEnd] = byte(sum % 256)
	out[dataEnd+1] = byte(sum / 256)

	if _, err := protocol.ParseStatusFrame(out); err != nil {
		t.Fatalf("synthesized status frame is invalid: %v", err)
	}
	return out
}

// Tapped telemetry must go stale on its own when the client stops polling. The
// guarantee cannot depend on any particular client's goodwill about how often
// it asks for status.
func TestRawPassthroughTapGoesStaleWhenClientStopsPolling(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer handle.Close()

	if handle.TapIsFresh() {
		t.Fatal("tap must not report fresh before any status frame is observed")
	}

	handle.ObserveFromAmp(statusFrameAtTemperature(t, 40))
	if !handle.TapIsFresh() {
		t.Fatal("tap should report fresh immediately after a status frame")
	}

	// Age the last observation past the contact window without waiting it out.
	handle.mu.Lock()
	handle.lastStatusAt = time.Now().Add(-2 * RecentContactWindow)
	handle.mu.Unlock()

	if handle.TapIsFresh() {
		t.Fatal("tap must go stale once the client stops supplying status frames")
	}
}

// A passthrough session that tears down late must not clear the reservation a
// newer session has since taken. Release is scoped to the exact lease instance,
// so the stale release is a no-op and the newer holder keeps ownership.
func TestRawPassthroughStaleReleaseCannotClearNewerLease(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)
	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	coordinator := transport.NewActuationCoordinator(src)
	owner := coordinator.Owner(transport.ActuationOwnerRawPassthrough, false)

	stale := owner.Acquire()
	if stale == nil {
		t.Fatal("first lease acquire failed")
	}
	stale.Release()

	current := owner.Acquire()
	if current == nil {
		t.Fatal("second lease acquire failed after the first was released")
	}

	// The first session's teardown runs late, after the second already owns
	// actuation. Release is idempotent, so this must not touch the newer lease.
	stale.Release()

	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "operate"}); transport.ButtonStatusCode(err) != 409 {
		t.Fatalf("a stale release cleared the newer session's ownership: expected 409, got %v", err)
	}

	current.Release()
}

// A rejected second client must not disturb the first client's actuation lease.
// Before v0.4.8 Acquire was re-entrant for the same owner name, so both
// sessions passed it and the loser's Release cleared the winner's ownership.
// The coordinator now refuses the second acquisition outright and scopes
// Release to the exact lease instance; this asserts both halves.
func TestRawPassthroughRejectedClientKeepsFirstLease(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)
	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	coordinator := transport.NewActuationCoordinator(src)
	owner := coordinator.Owner(transport.ActuationOwnerRawPassthrough, false)

	firstLease := owner.Acquire()
	if firstLease == nil {
		t.Fatal("first lease acquire failed")
	}
	first, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer first.Close()

	// The same owner name must not acquire twice. If it did, the second
	// lease's Release would be free to clear the first session's ownership.
	if second := owner.Acquire(); second != nil {
		second.Release()
		t.Fatal("second acquire returned a lease while the first was still held")
	}

	// The second client is rejected by the claim, and must not touch the lease.
	if _, err := src.BeginRawPassthrough(context.Background()); !errors.Is(err, ErrRawPassthroughBusy) {
		t.Fatalf("expected ErrRawPassthroughBusy, got %v", err)
	}

	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "operate"}); transport.ButtonStatusCode(err) != 409 {
		t.Fatalf("first client's lease was lost: expected 409 from the coordinator, got %v", err)
	}
}

// currentPortGeneration reads the live session generation the way the read loop
// stamps it, so a test can author a status frame that belongs to the real
// session rather than inventing a generation.
func currentPortGeneration(src *SerialSource) uint64 {
	src.portMu.RLock()
	defer src.portMu.RUnlock()
	return src.portGeneration
}

// The menu-debug authorization boundary, which is the open verification question
// on this feature: tapped telemetry must never become authority for server-side
// actuation, including across a session transition.
//
// Fan policy and overtemperature standby each test the provenance string for
// themselves, and TestPassthroughTapVisibleForDisplayRejectedForActuation pins
// that. Menu debug does not test provenance at all. It is safe for two
// structural reasons instead, and neither was pinned by a test: tapped frames
// are never handed to its controller, and a new serial session invalidates the
// evidence it already holds. Both are load-bearing, so both are asserted here --
// if a later change routes tapped status into ObserveStatus, or drops the
// session-generation invalidation, this fails rather than silently handing
// display-only evidence to an actuation gate.
func TestPassthroughTapNeverAuthorizesMenuDebugActuation(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	menuDebug := menudebug.NewController(nil)
	src.ConfigureMenuDebugController(menuDebug)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	// Genuine pre-lease evidence, from the live session, by the production path.
	// This is the only kind of status menu debug may ever act on.
	preLeaseGeneration := currentPortGeneration(src)
	src.applyStatusFrameFromSession(statusFrameAtTemperature(t, 30), preLeaseGeneration)

	before := menuDebug.Runtime()
	if before.Status.Provenance != "status-poll" {
		t.Fatalf("pre-lease provenance = %q, want %q", before.Status.Provenance, "status-poll")
	}
	if before.StatusGeneration == 0 {
		t.Fatal("pre-lease status evidence was not recorded, so the rest of this test would prove nothing")
	}
	if before.Status.ModelName == "" {
		t.Fatal("pre-lease evidence carries no model, so no write could be authorized with it")
	}

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer handle.Close()

	// A tapped frame the server never asked for, hot enough to trip protection
	// and in a state that would otherwise satisfy the STANDBY/RX prerequisites.
	handle.ObserveFromAmp(statusFrameAtTemperature(t, 80))

	// It reaches the display path. That is the point of the tap.
	if tapped := src.statusState.CurrentProtocolNative(); tapped.Provenance != ProvenancePassthroughTap {
		t.Fatalf("tapped status provenance = %q, want %q", tapped.Provenance, ProvenancePassthroughTap)
	}

	// It must not reach menu debug, which would accept it on its face.
	during := menuDebug.Runtime()
	if during.Status.Provenance == ProvenancePassthroughTap {
		t.Fatal("tapped status reached the menu-debug controller, which performs no provenance check of its own")
	}
	if during.StatusGeneration != before.StatusGeneration {
		t.Fatalf("menu-debug status evidence advanced on tapped traffic: generation %d -> %d", before.StatusGeneration, during.StatusGeneration)
	}
	if during.Status.TemperatureC != nil && *during.Status.TemperatureC >= 80 {
		t.Fatalf("tapped temperature became menu-debug evidence: %v", *during.Status.TemperatureC)
	}

	// The write path menu-debug actuation actually uses is the serial-session
	// authorized one, not SendButton. It must refuse outright while the lease is
	// held, and must not block behind the lease for the client's whole session.
	authorization := transport.SerialSessionWriteAuthorization{
		SessionGeneration: preLeaseGeneration,
		Model:             before.Status.ModelName,
	}
	writeDone := make(chan error, 1)
	go func() {
		_, sendErr := src.SendButtonForSerialSession(context.Background(), api.ButtonAction{Name: "operate"}, authorization)
		writeDone <- sendErr
	}()
	select {
	case sendErr := <-writeDone:
		if sendErr == nil {
			t.Fatal("a serial-session authorized write succeeded while passthrough held the port")
		}
		if transport.ButtonStatusCode(sendErr) != 409 {
			t.Fatalf("serial-session write: expected HTTP 409, got %d (%v)", transport.ButtonStatusCode(sendErr), sendErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serial-session authorized write blocked while a raw passthrough session held the port")
	}

	// The session transition. Closing the lease returns the port and reopens it
	// as a new serial session, and that must invalidate the evidence menu debug
	// was holding. Otherwise pre-lease status stays "recent" across a gap in
	// which an external client had exclusive control of the amplifier.
	handle.Close()
	waitForCondition(t, 2*time.Second, func() bool { return opener.openCount() == 3 })
	waitForCondition(t, 2*time.Second, func() bool {
		return menuDebug.Runtime().SerialSessionGeneration > before.SerialSessionGeneration
	})

	after := menuDebug.Runtime()
	if after.Status.RecentContact {
		t.Fatal("menu-debug evidence still reads as recent contact after a passthrough session")
	}
	if !after.StatusObservedAt.IsZero() {
		t.Fatalf("menu-debug status observation survived the session change: %v", after.StatusObservedAt)
	}
}

// displaySnapshot is what the display path has to offer Resolve: telemetry
// decoded from display frames, with no protocol-native fields of its own.
func displaySnapshot(band string, updatedAt time.Time) Snapshot {
	return Snapshot{
		Telemetry: api.Telemetry{
			Band:           band,
			OperatingState: "operate",
			Source:         "serial",
			Confidence:     "display-derived",
			Provenance:     "display-frame",
		},
		UpdatedAt: updatedAt,
	}
}

// A lease stops the server's own polling, so the status-poll frame it was
// holding stops being evidence of anything current. Serving it anyway is how
// /api/v1/status and /api/v1/alarms came to report pre-lease temperature, SWR,
// TX and output level as "status-poll" with recentContact true -- for a client
// that may never ask the amplifier for status at all, which is exactly what SPE
// Expert Controller Plus does.
//
// The window is what makes this worth pinning: a lease can begin within
// RecentContactWindow of the last poll, so waiting for the retained frame to age
// out is not the same as dropping it, and the display-only case never ages into
// correctness at all -- it stays wrong for the life of the lease.
func TestPassthroughLeaseStopsServingPreLeaseStatusAsCanonical(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	// A real poll reply, seconds old at most: canonical status is entitled to
	// report it, and does. Without this the rest of the test proves nothing.
	src.applyStatusFrame(statusFrameAtTemperature(t, 40))
	before := src.statusState.Resolve(displaySnapshot("20m", time.Now().UTC()))
	if before.Provenance != "status-poll" {
		t.Fatalf("pre-lease provenance = %q, want status-poll", before.Provenance)
	}
	if before.TemperatureC == nil || *before.TemperatureC != 40 {
		t.Fatalf("pre-lease temperature = %v, want the polled 40", before.TemperatureC)
	}
	if !before.RecentContact {
		t.Fatal("pre-lease status does not report recent contact, so this test cannot show the lease changing that")
	}

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}

	// Immediately, with the polled frame still inside its contact window.
	atLeaseStart := src.statusState.Resolve(Snapshot{})
	if atLeaseStart.Provenance == "status-poll" {
		t.Fatal("canonical status still claims status-poll provenance after the server stopped polling")
	}
	if atLeaseStart.TemperatureC != nil {
		t.Fatalf("pre-lease temperature %v is still being served as canonical during the lease", *atLeaseStart.TemperatureC)
	}
	if atLeaseStart.RecentContact {
		t.Fatal("canonical status reports recent contact for a reading nothing is refreshing")
	}

	// A display-only client: display frames flow, no 0x90 is ever sent. The tap
	// must still be working, or the assertions below would pass for the wrong
	// reason.
	handle.ObserveFromAmp(displayStreamChunk(t))
	stats := handle.Stats()
	if stats.DisplayFrames == 0 {
		t.Fatal("no display frames were tapped, so this is not the display-only case")
	}
	if stats.StatusFramesSeen != 0 {
		t.Fatalf("statusFramesSeen = %d, want 0 for a client that never polls status", stats.StatusFramesSeen)
	}
	if handle.TapIsFresh() {
		t.Fatal("tap reports fresh without a single tapped status frame")
	}

	// Display state changes during the lease and canonical status follows it --
	// display contact is real contact, so recentContact true here is honest. What
	// must not come back are the protocol-only fields.
	duringLease := src.statusState.Resolve(displaySnapshot("40m", time.Now().UTC()))
	if duringLease.Band != "40m" {
		t.Fatalf("band = %q, want the display change to be reflected during the lease", duringLease.Band)
	}
	if duringLease.Provenance != "display-frame" {
		t.Fatalf("provenance = %q, want display-frame while only the display is being tapped", duringLease.Provenance)
	}
	// The display snapshot in this test carries no temperature or TX of its own,
	// so anything here could only have come from the invalidated status frame.
	// On real hardware the amplifier prints both on its LCD, and they do keep
	// being reported during a lease -- as display-derived values, which is the
	// point: the label tells the consumer which it is looking at.
	if duringLease.TemperatureC != nil || duringLease.TX != nil {
		t.Fatalf("the invalidated status frame leaked into canonical status: temp=%v tx=%v", duringLease.TemperatureC, duringLease.TX)
	}

	// Disconnect. The port comes back but the first new poll reply has not
	// arrived yet, and the pre-lease frame is no more current now than it was
	// during the lease -- an external client had exclusive control in between.
	handle.Close()
	waitForCondition(t, 2*time.Second, func() bool { return opener.openCount() >= 3 })

	afterClose := src.statusState.Resolve(displaySnapshot("40m", time.Now().UTC()))
	if afterClose.Provenance == "status-poll" || afterClose.TemperatureC != nil {
		t.Fatalf("pre-lease status came back after disconnect, before any new poll reply: %+v", afterClose)
	}

	// The first poll reply after the lease restores canonical status on its own.
	// Nothing had to be restored by the teardown path.
	src.applyStatusFrame(statusFrameAtTemperature(t, 41))
	recovered := src.statusState.Resolve(displaySnapshot("40m", time.Now().UTC()))
	if recovered.Provenance != "status-poll" {
		t.Fatalf("provenance = %q, want status-poll once polling resumes", recovered.Provenance)
	}
	if recovered.TemperatureC == nil || *recovered.TemperatureC != 41 {
		t.Fatalf("temperature = %v, want the newly polled 41", recovered.TemperatureC)
	}
	if !recovered.RecentContact {
		t.Fatal("canonical status does not report recent contact after a fresh poll reply")
	}
}

// The other way out of the invalidated state: a client that does poll 0x90.
// Its replies are tapped, labelled passthrough-tap, and are good enough to
// display -- so canonical status comes back on the tapped frame, still carrying
// the label that every actuation gate refuses.
func TestPassthroughTappedStatusRestoresCanonicalStatusDuringLease(t *testing.T) {
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{&mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	src.applyStatusFrame(statusFrameAtTemperature(t, 40))

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer handle.Close()

	if resolved := src.statusState.Resolve(Snapshot{}); resolved.TemperatureC != nil {
		t.Fatalf("pre-lease temperature %v survived the start of the lease", *resolved.TemperatureC)
	}

	handle.ObserveFromAmp(statusFrameAtTemperature(t, 80))

	resolved := src.statusState.Resolve(displaySnapshot("20m", time.Now().UTC()))
	if resolved.Provenance != ProvenancePassthroughTap {
		t.Fatalf("provenance = %q, want %q once the client's own poll reply is tapped", resolved.Provenance, ProvenancePassthroughTap)
	}
	if resolved.TemperatureC == nil || *resolved.TemperatureC != 80 {
		t.Fatalf("temperature = %v, want the tapped 80", resolved.TemperatureC)
	}
	if !resolved.RecentContact {
		t.Fatal("a just-tapped status frame does not report recent contact")
	}
}

// eofOnCloseSerialPort blocks in Read until the port is closed and then reports
// io.EOF rather than a transport error. readFromPort treats EOF as "keep going",
// so the read loop returns to the top of its loop and asks for its next poll
// instead of unwinding -- which is precisely the state the deadlock needs, and
// reaching it this way makes the test deterministic instead of timing-dependent.
type eofOnCloseSerialPort struct {
	mu           sync.Mutex
	closed       bool
	closedCh     chan struct{}
	closeOnce    sync.Once
	writeStarted chan struct{}
	writes       int
}

func (p *eofOnCloseSerialPort) closedChan() chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closedCh == nil {
		p.closedCh = make(chan struct{})
	}
	return p.closedCh
}

func (p *eofOnCloseSerialPort) Read(buf []byte) (int, error) {
	<-p.closedChan()
	return 0, io.EOF
}

func (p *eofOnCloseSerialPort) Write(buf []byte) (int, error) {
	p.mu.Lock()
	p.writes++
	p.mu.Unlock()
	if p.writeStarted != nil {
		select {
		case p.writeStarted <- struct{}{}:
		default:
		}
	}
	return len(buf), nil
}

func (p *eofOnCloseSerialPort) Close() error {
	closedCh := p.closedChan()
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.closeOnce.Do(func() { close(closedCh) })
	return nil
}

func (p *eofOnCloseSerialPort) SetReadTimeout(time.Duration) error { return nil }
func (p *eofOnCloseSerialPort) SetDTR(bool) error                  { return nil }
func (p *eofOnCloseSerialPort) SetRTS(bool) error                  { return nil }

// A scheduled poll waiting to enter writeFrameForSerialSession must not be able
// to wedge lease acquisition.
//
// The retire waits for the read loop to unwind, and the read loop cannot unwind
// until it gets writeMu, so holding writeMu across the retire deadlocks the two
// against each other until the retire times out and refuses a client that should
// have been admitted. Marking the transition under writeMu and releasing it
// before the retire is what breaks the cycle: the parked poll acquires the
// mutex, observes the flag, fails closed, and that failure is what ends the
// session the retire is waiting for.
func TestBeginRawPassthroughIsNotWedgedByAPollWaitingOnWriteMu(t *testing.T) {
	live := &eofOnCloseSerialPort{writeStarted: make(chan struct{}, 1)}
	leased := &mockSerialPort{blockRead: true}
	reconnected := &mockSerialPort{blockRead: true}
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{live, leased, reconnected},
		opened: make(chan int, 3),
	}
	// A real poll frame and a short interval, so the read loop actually polls and
	// a poll is due again the moment it returns to the top of its loop.
	src := NewSerialSource(SerialSourceConfig{
		Port:                      "/dev/ttyTEST0",
		BaudRate:                  115200,
		ReadTimeout:               10 * time.Millisecond,
		ReadSize:                  512,
		MinFrameLen:               64,
		MaxBuffer:                 8192,
		IOTimeout:                 2 * time.Second,
		ReconnectBackoff:          100 * time.Millisecond,
		PollingMode:               "status",
		PollInterval:              time.Millisecond,
		StatusPollCommandEnabled:  true,
		StatusPollCommandFrameHex: "55555502",
	}, opener, Update{})

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	// Wait until the read loop has issued at least one poll and settled into
	// Read. From here, closing the port sends it straight back to the poll.
	select {
	case <-live.writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("read loop never issued a scheduled poll")
	}

	beginCtx, beginCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer beginCancel()

	done := make(chan error, 1)
	var handle *RawPassthroughHandle
	go func() {
		h, err := src.BeginRawPassthrough(beginCtx)
		handle = h
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BeginRawPassthrough refused a valid client: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("BeginRawPassthrough never returned: the retire and the parked poll deadlocked")
	}

	if handle == nil {
		t.Fatal("no handle returned")
	}
	if handle.Port() != serial.Port(leased) {
		t.Fatal("passthrough did not return the newly opened port")
	}
	handle.Close()
}

// The transition has to be visible to writers from the moment acquisition
// starts, not from the moment it finishes. This pins the property the deadlock
// fix depends on, without depending on the read loop's timing.
func TestRawPassthroughTransitionRefusesWritesBeforeThePortIsInHand(t *testing.T) {
	live := &mockSerialPort{blockRead: true}
	leased := &mockSerialPort{blockRead: true}
	opener := &sequenceSerialOpener{
		ports:  []serial.Port{live, leased, &mockSerialPort{blockRead: true}},
		opened: make(chan int, 3),
	}
	src := newPassthroughTestSource(t, opener)

	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	defer cancel()
	waitForCondition(t, time.Second, func() bool { return opener.openCount() == 1 })

	handle, err := src.BeginRawPassthrough(context.Background())
	if err != nil {
		t.Fatalf("BeginRawPassthrough error: %v", err)
	}
	defer handle.Close()

	// A server write during the lease fails closed rather than blocking. The
	// same flag is what a poll parked mid-transition observes.
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- src.writeFrameForSerialSession(context.Background(), []byte{0x90}, transport.SerialSessionWriteAuthorization{})
	}()
	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("write succeeded during a lease, want a refusal")
		}
		if transport.ButtonStatusCode(err) != 409 {
			t.Fatalf("write error = %v (status %d), want the 409 raw-passthrough-active refusal", err, transport.ButtonStatusCode(err))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server write blocked instead of failing closed during a lease")
	}
}
