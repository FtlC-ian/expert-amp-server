package rawpassthrough

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/monitoring"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

func hotDirectPoll(t *testing.T) []byte {
	t.Helper()
	frame, err := os.ReadFile("../protocol/testdata/status_response_example.bin")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(string(frame[4:71]), ",")
	fields[1], fields[14], fields[15], fields[16] = "O", "080", "080", "080"
	copy(frame[4:71], strings.Join(fields, ","))
	sum := 0
	for _, b := range frame[4:71] {
		sum += int(b)
	}
	frame[71], frame[72] = byte(sum%256), byte(sum/256)
	if _, err := protocol.ParseStatusFrame(frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestPassthroughAdmissionDefersInFlightHotPollWithoutSpendingSafetyLatch(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()
	src := controller.cfg.Source
	coordinator := transport.NewActuationCoordinator(src)
	safety := monitoring.NewController(coordinator.Owner(transport.ActuationOwnerSafety, true))
	settings := monitoring.ControlSettings{Enabled: true, Armed: true,
		Thresholds: monitoring.Thresholds{TemperatureTripC: 50, TemperatureResetC: 40}}
	observing, resume := make(chan struct{}), make(chan struct{})
	var first, unblock sync.Once
	defer unblock.Do(func() { close(resume) })
	src.ConfigureSafetyController(safety, func() monitoring.ControlSettings {
		first.Do(func() {
			close(observing)
			<-resume
		})
		return settings
	})
	controller.cfg.Lease = coordinator.Owner(transport.ActuationOwnerRawPassthrough, false)
	controller.cfg.ArmedAutomaticControls = func() []string { return []string{"overtemperature standby"} }
	controller.cfg.AllowWhileArmed = func() bool { return true }
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)
	live := opener.waitForNewPort(t, 0)
	hot := hotDirectPoll(t)
	live.toHost <- hot
	select {
	case <-observing:
	case <-time.After(2 * time.Second):
		t.Fatal("direct poll did not reach safety observation")
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The closed live port proves admission owns the coordinator and has
	// marked passthrough active while this poll still holds up retirement.
	select {
	case <-live.done:
	case <-time.After(2 * time.Second):
		t.Fatal("admission did not start retiring the live session")
	}
	unblock.Do(func() { close(resume) })
	waitForLease(t, controller)
	if result := safety.Apply(monitoring.Result{}, true); result.Latched || result.Action.State != monitoring.ActionIdle {
		t.Fatalf("in-flight poll spent safety latch during admission: %+v", result)
	}
	for _, port := range []*fakePort{live, opener.latest()} {
		if len(port.fromHost) != 0 {
			t.Fatal("server wrote during passthrough admission")
		}
	}
	if _, err := coordinator.SendButton(ctx, api.ButtonAction{Name: "operate"}); transport.ButtonStatusCode(err) != 409 {
		t.Fatalf("raw coordinator reservation lost: %v", err)
	}
	baseline := opener.count()
	_ = conn.Close()
	reclaimed := opener.waitForNewPort(t, baseline)
	deadline := time.Now().Add(2 * time.Second)
	for controller.Status().ClientConnected {
		if time.Now().After(deadline) {
			t.Fatal("passthrough teardown did not release ownership")
		}
		time.Sleep(time.Millisecond)
	}
	reclaimed.toHost <- hot
	select {
	case got := <-reclaimed.fromHost:
		if !bytes.Equal(got, []byte{0x55, 0x55, 0x55, 0x01, 0x0d, 0x0d}) {
			t.Fatalf("reclaimed write = %x, want standby toggle", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first fresh hot direct poll after reclaim did not trigger standby")
	}
	if result := safety.Apply(monitoring.Result{}, true); !result.Latched {
		t.Fatal("fresh hot poll did not latch safety")
	}
}

func TestPassthroughAdmissionRefusesExistingSafetyHold(t *testing.T) {
	controller, opener, cancel := newTestController(t)
	defer cancel()
	coordinator := transport.NewActuationCoordinator(controller.cfg.Source)
	safety := coordinator.Owner(transport.ActuationOwnerSafety, true)
	lease := safety.Acquire()
	if lease == nil {
		t.Fatal("safety did not acquire coordinator")
	}
	defer lease.Release()
	controller.cfg.Lease = coordinator.Owner(transport.ActuationOwnerRawPassthrough, false)
	controller.cfg.ArmedAutomaticControls = func() []string { return []string{"overtemperature standby"} }
	controller.cfg.AllowWhileArmed = func() bool { return true }
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	addr := startListener(t, ctx, controller)
	live := opener.waitForNewPort(t, 0)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("client with existing safety hold was not cleanly refused: %v", err)
	}
	if opener.count() != 1 || controller.Status().ClientConnected {
		t.Fatal("refused client changed serial ownership")
	}
	if _, err := safety.SendButton(ctx, api.ButtonAction{Name: "operate"}); err != nil {
		t.Fatalf("safety could not complete after refusing passthrough: %v", err)
	}
	select {
	case got := <-live.fromHost:
		if !bytes.Equal(got, []byte{0x55, 0x55, 0x55, 0x01, 0x0d, 0x0d}) {
			t.Fatalf("safety write = %x, want standby toggle", got)
		}
	default:
		t.Fatal("safety write never reached live serial port")
	}
}
