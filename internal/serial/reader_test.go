package serial

import (
	"context"
	"strings"
	"testing"
	"time"
)

// mockPort implements Port for testing.
type mockPort struct {
	chunks   [][]byte
	chunkIdx int
	closed   bool
	written  [][]byte
	dtrSet   bool
	rtsSet   bool
}

func (m *mockPort) Read(buf []byte) (int, error) {
	if m.chunkIdx >= len(m.chunks) {
		// Block briefly so the read loop can check context.
		time.Sleep(10 * time.Millisecond)
		return 0, nil
	}
	chunk := m.chunks[m.chunkIdx]
	m.chunkIdx++
	n := copy(buf, chunk)
	return n, nil
}

func (m *mockPort) Write(buf []byte) (int, error) {
	m.written = append(m.written, append([]byte(nil), buf...))
	return len(buf), nil
}

func (m *mockPort) Close() error {
	m.closed = true
	return nil
}

func (m *mockPort) SetReadTimeout(time.Duration) error { return nil }
func (m *mockPort) SetDTR(v bool) error {
	m.dtrSet = v
	return nil
}
func (m *mockPort) SetRTS(v bool) error {
	m.rtsSet = v
	return nil
}

// mockOpener implements PortOpener for testing.
type mockOpener struct {
	port *mockPort
}

func (m *mockOpener) Open(string, int) (Port, error) {
	return m.port, nil
}

func TestReadLoopStreamsChunks(t *testing.T) {
	port := &mockPort{
		chunks: [][]byte{
			{0x01, 0x02, 0x03},
			{0x04, 0x05},
		},
	}
	opener := &mockOpener{port: port}

	var received [][]byte
	cfg := Config{
		Port:        "/dev/ttyTEST",
		BaudRate:    115200,
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := ReadLoop(ctx, opener, cfg, func(chunk []byte) {
		received = append(received, append([]byte(nil), chunk...))
	})
	if err != nil {
		t.Fatalf("ReadLoop error: %v", err)
	}

	if len(received) < 1 {
		t.Fatalf("received %d chunks, want at least 1", len(received))
	}
	if !port.closed {
		t.Fatal("port was not closed after ReadLoop")
	}
}

func TestReadLoopSendsPollFrame(t *testing.T) {
	port := &mockPort{chunks: [][]byte{{0xAA}}}
	opener := &mockOpener{port: port}

	cfg := Config{
		Port:         "/dev/ttyTEST",
		BaudRate:     115200,
		ReadTimeout:  10 * time.Millisecond,
		ReadSize:     512,
		PollFrame:    []byte{0x55, 0x55, 0x55},
		PollInterval: 20 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_ = ReadLoop(ctx, opener, cfg, func(chunk []byte) {})

	if len(port.written) == 0 {
		t.Fatal("expected poll frame to be written")
	}
	first := port.written[0]
	if len(first) != 3 || first[0] != 0x55 || first[1] != 0x55 || first[2] != 0x55 {
		t.Fatalf("poll frame = %v, want [0x55 0x55 0x55]", first)
	}
}

func TestReadLoopAssertsDTRAndRTS(t *testing.T) {
	port := &mockPort{chunks: [][]byte{}}
	opener := &mockOpener{port: port}

	cfg := Config{
		Port:        "/dev/ttyTEST",
		BaudRate:    115200,
		ReadTimeout: 10 * time.Millisecond,
		ReadSize:    512,
		AssertDTR:   true,
		AssertRTS:   true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_ = ReadLoop(ctx, opener, cfg, func(chunk []byte) {})

	if !port.dtrSet {
		t.Fatal("DTR was not asserted")
	}
	if !port.rtsSet {
		t.Fatal("RTS was not asserted")
	}
}

func TestReadLoopRequiresPort(t *testing.T) {
	cfg := Config{Port: ""}
	err := ReadLoop(context.Background(), &mockOpener{}, cfg, func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "serial port path is required") {
		t.Fatalf("error = %v, want missing port error", err)
	}
}

func TestConfigNormalized(t *testing.T) {
	cfg := Config{}
	norm := cfg.Normalized()
	if norm.BaudRate != 115200 {
		t.Fatalf("BaudRate = %d, want 115200", norm.BaudRate)
	}
	if norm.ReadSize != 512 {
		t.Fatalf("ReadSize = %d, want 512", norm.ReadSize)
	}
	if norm.ReadTimeout != 250*time.Millisecond {
		t.Fatalf("ReadTimeout = %v, want 250ms", norm.ReadTimeout)
	}
}
