package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

type recordingActuator struct {
	mu             sync.Mutex
	actions        []string
	authorizations []SerialSessionWriteAuthorization
	wakes          int
	wakeStarted    chan struct{}
	wakeRelease    chan struct{}
}

func (r *recordingActuator) SendButton(_ context.Context, action api.ButtonAction) (api.ActionResult, error) {
	r.actions = append(r.actions, action.Name)
	return api.ActionResult{Name: action.Name, Sent: true}, nil
}

func (r *recordingActuator) SendButtonForSerialSession(_ context.Context, action api.ButtonAction, authorization SerialSessionWriteAuthorization) (api.ActionResult, error) {
	r.actions = append(r.actions, action.Name)
	r.authorizations = append(r.authorizations, authorization)
	return api.ActionResult{Name: action.Name, Sent: true}, nil
}

func (r *recordingActuator) SendWake(_ context.Context) (api.ActionResult, error) {
	if r.wakeStarted != nil {
		close(r.wakeStarted)
	}
	if r.wakeRelease != nil {
		<-r.wakeRelease
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wakes++
	return api.ActionResult{Name: "wake", Sent: true}, nil
}

func TestActuationCoordinatorRejectsManualWritesDuringAutomaticLease(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	fan := coordinator.Owner(ActuationOwnerFan, false)
	fanLease := fan.Acquire()
	if fanLease == nil {
		t.Fatal("fan lease was not acquired")
	}

	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err == nil {
		t.Fatal("manual write was accepted during fan transaction")
	} else if actionErr, ok := err.(*ButtonActionError); !ok || actionErr.StatusCode != 409 {
		t.Fatalf("manual rejection = %T %v, want HTTP 409 ButtonActionError", err, err)
	}
	if len(raw.actions) != 0 {
		t.Fatalf("rejected manual write reached actuator: %v", raw.actions)
	}

	fanLease.Release()
	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err != nil {
		t.Fatalf("manual write after release failed: %v", err)
	}
}

func TestActuationCoordinatorSafetyPreemptsFanAndHoldsActuator(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	fan := coordinator.Owner(ActuationOwnerFan, false)
	safety := coordinator.Owner(ActuationOwnerSafety, true)
	fanLease := fan.Acquire()
	if fanLease == nil {
		t.Fatal("fan lease was not acquired")
	}
	safetyLease := safety.Acquire()
	if safetyLease == nil {
		t.Fatal("safety lease was not acquired")
	}
	if !safety.SafetyHold() {
		t.Fatal("safety acquisition did not establish a safety hold")
	}
	fanLease.Release()

	if _, err := fan.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err == nil {
		t.Fatal("preempted fan owner was allowed to write")
	}
	if fan.Acquire() != nil {
		t.Fatal("fan reacquired actuator during safety hold")
	}
	if _, err := safety.SendButton(context.Background(), api.ButtonAction{Name: "operate"}); err != nil {
		t.Fatalf("safety write failed: %v", err)
	}
	if len(raw.actions) != 1 || raw.actions[0] != "operate" {
		t.Fatalf("raw actions = %v, want one safety operate toggle", raw.actions)
	}

	safetyLease.Release()
	if safety.SafetyHold() {
		t.Fatal("safety hold remained active after its lease released")
	}
	if fan.Acquire() == nil {
		t.Fatal("fan could not acquire actuator after safety reset")
	}
}

func TestActuationCoordinatorRejectsWakeDuringAutomaticLease(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	wake := coordinator.GateWake(raw)
	fan := coordinator.Owner(ActuationOwnerFan, false)
	fanLease := fan.Acquire()
	if fanLease == nil {
		t.Fatal("fan lease was not acquired")
	}
	if _, err := wake.SendWake(context.Background()); err == nil {
		t.Fatal("wake was accepted during fan transaction")
	}
	if raw.wakes != 0 {
		t.Fatalf("rejected wake reached transport %d times", raw.wakes)
	}
	fanLease.Release()
	if _, err := wake.SendWake(context.Background()); err != nil {
		t.Fatalf("wake after release failed: %v", err)
	}
	if raw.wakes != 1 {
		t.Fatalf("wake count = %d, want 1", raw.wakes)
	}
}

func TestActuationCoordinatorReservesWakeWithoutHoldingMutex(t *testing.T) {
	raw := &recordingActuator{wakeStarted: make(chan struct{}), wakeRelease: make(chan struct{})}
	coordinator := NewActuationCoordinator(raw)
	wake := coordinator.GateWake(raw)
	menu := coordinator.Owner(ActuationOwnerMenuDebug, false)
	safety := coordinator.Owner(ActuationOwnerSafety, true)
	staleMenuLease := menu.Acquire()
	if staleMenuLease == nil {
		t.Fatal("menu-debug lease was not acquired for stale-release setup")
	}
	staleMenuLease.Release()

	wakeDone := make(chan error, 1)
	go func() {
		_, err := wake.SendWake(context.Background())
		wakeDone <- err
	}()
	select {
	case <-raw.wakeStarted:
	case <-time.After(time.Second):
		t.Fatal("wake transport did not start")
	}

	acquireDone := make(chan ActuationLease, 1)
	go func() { acquireDone <- menu.Acquire() }()
	select {
	case acquired := <-acquireDone:
		if acquired != nil {
			t.Fatal("menu-debug acquired the actuator during wake")
		}
	case <-time.After(time.Second):
		t.Fatal("menu-debug acquire blocked behind wake")
	}

	releaseDone := make(chan struct{})
	go func() {
		staleMenuLease.Release()
		close(releaseDone)
	}()
	select {
	case <-releaseDone:
	case <-time.After(time.Second):
		t.Fatal("stale lease release blocked behind wake")
	}

	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err == nil {
		t.Fatal("manual write was accepted during wake")
	}
	if _, err := wake.SendWake(context.Background()); err == nil {
		t.Fatal("second wake was accepted during wake")
	}

	safetyDone := make(chan ActuationLease, 1)
	go func() { safetyDone <- safety.Acquire() }()
	select {
	case <-safetyDone:
		t.Fatal("safety acquisition did not wait for wake")
	case <-time.After(50 * time.Millisecond):
	}

	close(raw.wakeRelease)
	select {
	case err := <-wakeDone:
		if err != nil {
			t.Fatalf("wake failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wake did not finish")
	}
	select {
	case acquired := <-safetyDone:
		if acquired == nil {
			t.Fatal("safety did not acquire the actuator after wake")
		}
	case <-time.After(time.Second):
		t.Fatal("safety did not resume after wake")
	}
}

func TestOwnedActuationForwardsSerialSessionBinding(t *testing.T) {
	raw := &recordingActuator{}
	menu := NewActuationCoordinator(raw).Owner(ActuationOwnerMenuDebug, false)
	if menu.Acquire() == nil {
		t.Fatal("menu-debug lease was not acquired")
	}
	sessionTransport, ok := menu.(SerialSessionButtonTransport)
	if !ok {
		t.Fatal("owned transport does not preserve serial-session binding")
	}
	authorization := SerialSessionWriteAuthorization{SessionGeneration: 7, Model: "EXPERT 1.3K-FA"}
	if _, err := sessionTransport.SendButtonForSerialSession(context.Background(), api.ButtonAction{Name: "set"}, authorization); err != nil {
		t.Fatal(err)
	}
	if len(raw.actions) != 1 || raw.actions[0] != "set" || len(raw.authorizations) != 1 || raw.authorizations[0] != authorization {
		t.Fatalf("forwarded actions=%v authorizations=%v", raw.actions, raw.authorizations)
	}
}

func TestActuationCoordinatorUsesLeaseInstanceIdentity(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	firstOwner := coordinator.Owner("raw-passthrough", false)
	secondOwner := coordinator.Owner("raw-passthrough", false)

	firstLease := firstOwner.Acquire()
	if firstLease == nil {
		t.Fatal("first same-named owner did not acquire the actuator")
	}
	if firstOwner.Acquire() != nil {
		t.Fatal("same owner view acquired a second concurrent lease")
	}
	if secondOwner.Acquire() != nil {
		t.Fatal("second same-named owner acquired the active lease")
	}

	firstLease.Release()
	secondLease := secondOwner.Acquire()
	if secondLease == nil {
		t.Fatal("second same-named owner did not acquire after release")
	}

	firstLease.Release()
	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err == nil {
		t.Fatal("stale release cleared a newer same-named lease")
	}
	if _, err := firstOwner.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err == nil {
		t.Fatal("stale same-named owner was allowed to write")
	}
	if _, err := secondOwner.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err != nil {
		t.Fatalf("current same-named owner write failed: %v", err)
	}

	secondLease.Release()
	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err != nil {
		t.Fatalf("manual write after exact lease release failed: %v", err)
	}
}
