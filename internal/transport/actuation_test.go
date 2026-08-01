package transport

import (
	"context"
	"testing"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

type recordingActuator struct {
	actions  []string
	sessions []uint64
	wakes    int
}

func (r *recordingActuator) SendButton(_ context.Context, action api.ButtonAction) (api.ActionResult, error) {
	r.actions = append(r.actions, action.Name)
	return api.ActionResult{Name: action.Name, Sent: true}, nil
}

func (r *recordingActuator) SendButtonForSerialSession(_ context.Context, action api.ButtonAction, sessionGeneration uint64) (api.ActionResult, error) {
	r.actions = append(r.actions, action.Name)
	r.sessions = append(r.sessions, sessionGeneration)
	return api.ActionResult{Name: action.Name, Sent: true}, nil
}

func (r *recordingActuator) SendWake(_ context.Context) (api.ActionResult, error) {
	r.wakes++
	return api.ActionResult{Name: "wake", Sent: true}, nil
}

func TestActuationCoordinatorRejectsManualWritesDuringAutomaticLease(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	fan := coordinator.Owner(ActuationOwnerFan, false)
	if !fan.Acquire() {
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

	fan.Release()
	if _, err := coordinator.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err != nil {
		t.Fatalf("manual write after release failed: %v", err)
	}
}

func TestActuationCoordinatorSafetyPreemptsFanAndHoldsActuator(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	fan := coordinator.Owner(ActuationOwnerFan, false)
	safety := coordinator.Owner(ActuationOwnerSafety, true)
	if !fan.Acquire() {
		t.Fatal("fan lease was not acquired")
	}
	if !safety.Acquire() {
		t.Fatal("safety lease was not acquired")
	}
	safety.SetSafetyHold(true)

	if _, err := fan.SendButton(context.Background(), api.ButtonAction{Name: "set"}); err == nil {
		t.Fatal("preempted fan owner was allowed to write")
	}
	if fan.Acquire() {
		t.Fatal("fan reacquired actuator during safety hold")
	}
	if _, err := safety.SendButton(context.Background(), api.ButtonAction{Name: "operate"}); err != nil {
		t.Fatalf("safety write failed: %v", err)
	}
	if len(raw.actions) != 1 || raw.actions[0] != "operate" {
		t.Fatalf("raw actions = %v, want one safety operate toggle", raw.actions)
	}

	safety.SetSafetyHold(false)
	safety.Release()
	if !fan.Acquire() {
		t.Fatal("fan could not acquire actuator after safety reset")
	}
}

func TestActuationCoordinatorRejectsWakeDuringAutomaticLease(t *testing.T) {
	raw := &recordingActuator{}
	coordinator := NewActuationCoordinator(raw)
	wake := coordinator.GateWake(raw)
	fan := coordinator.Owner(ActuationOwnerFan, false)
	if !fan.Acquire() {
		t.Fatal("fan lease was not acquired")
	}
	if _, err := wake.SendWake(context.Background()); err == nil {
		t.Fatal("wake was accepted during fan transaction")
	}
	if raw.wakes != 0 {
		t.Fatalf("rejected wake reached transport %d times", raw.wakes)
	}
	fan.Release()
	if _, err := wake.SendWake(context.Background()); err != nil {
		t.Fatalf("wake after release failed: %v", err)
	}
	if raw.wakes != 1 {
		t.Fatalf("wake count = %d, want 1", raw.wakes)
	}
}

func TestOwnedActuationForwardsSerialSessionBinding(t *testing.T) {
	raw := &recordingActuator{}
	menu := NewActuationCoordinator(raw).Owner(ActuationOwnerMenuDebug, false)
	if !menu.Acquire() {
		t.Fatal("menu-debug lease was not acquired")
	}
	sessionTransport, ok := menu.(SerialSessionButtonTransport)
	if !ok {
		t.Fatal("owned transport does not preserve serial-session binding")
	}
	if _, err := sessionTransport.SendButtonForSerialSession(context.Background(), api.ButtonAction{Name: "set"}, 7); err != nil {
		t.Fatal(err)
	}
	if len(raw.actions) != 1 || raw.actions[0] != "set" || len(raw.sessions) != 1 || raw.sessions[0] != 7 {
		t.Fatalf("forwarded actions=%v sessions=%v", raw.actions, raw.sessions)
	}
}
