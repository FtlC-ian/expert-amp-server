// Package serial provides a cross-platform serial port read loop
// for communicating with SPE Expert amplifiers.
package serial

import (
	"context"
	"fmt"
	"io"
	"time"

	goserial "go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

// PortOpener abstracts serial port opening for testability.
// Production code uses [OpenRealPort].
type PortOpener interface {
	Open(port string, baudRate int) (Port, error)
}

// Port abstracts the serial port interface for testability.
type Port interface {
	Read(buf []byte) (int, error)
	Write(buf []byte) (int, error)
	Close() error
	SetReadTimeout(timeout time.Duration) error
	SetDTR(v bool) error
	SetRTS(v bool) error
}

// RealPort wraps a go.bug.st/serial.Port to satisfy the Port interface.
type RealPort struct {
	goserial.Port
}

func (p *RealPort) SetReadTimeout(timeout time.Duration) error {
	return p.Port.SetReadTimeout(timeout)
}

func (p *RealPort) SetDTR(v bool) error {
	return p.Port.SetDTR(v)
}

func (p *RealPort) SetRTS(v bool) error {
	return p.Port.SetRTS(v)
}

// OpenRealPort opens a real serial port using go.bug.st/serial.
type OpenRealPort struct{}

func (OpenRealPort) Open(port string, baudRate int) (Port, error) {
	mode := &goserial.Mode{BaudRate: baudRate}
	p, err := goserial.Open(port, mode)
	if err != nil {
		return nil, err
	}
	return &RealPort{Port: p}, nil
}

// PortInfo describes a discovered serial port.
type PortInfo struct {
	// Name is the OS device path (e.g. "/dev/ttyUSB1").
	Name string
	// ByIDPath is the stable /dev/serial/by-id/ path if available.
	ByIDPath string
	// SerialNumber is the USB serial number (e.g. "XXXXXXXX").
	SerialNumber string
	// Manufacturer is the USB manufacturer string.
	Manufacturer string
	// Product is the USB product string.
	Product string
	// IsFTDI is true when the port is an FTDI USB serial adapter.
	IsFTDI bool
}

// EnumeratePorts lists available serial ports with metadata including
// USB serial numbers, manufacturer, and product strings. This uses
// go.bug.st/serial/enumerator for rich discovery.
//
// On Linux, this reads /sys/bus/usb-serial/devices/ and udev attributes
// to identify each port's USB serial number, allowing stable identification
// of the SPE Expert control port across reboots.
func EnumeratePorts() ([]PortInfo, error) {
	enumPorts, err := enumerator.GetDetailedPortsList()
	if err != nil {
		// Fall back to basic enumeration if the detailed enumerator fails
		// (e.g. no udev on some systems).
		basics, err := goserial.GetPortsList()
		if err != nil {
			return nil, fmt.Errorf("enumerate serial ports: %w", err)
		}
		var result []PortInfo
		for _, p := range basics {
			result = append(result, PortInfo{Name: p})
		}
		return result, nil
	}

	var result []PortInfo
	for _, p := range enumPorts {
		info := PortInfo{
			Name:         p.Name,
			SerialNumber: p.SerialNumber,
			IsFTDI:       p.IsUSB && p.VID == "0403",
		}
		if p.IsUSB {
			info.Product = p.Product
			// Build a stable /dev/serial/by-id/ path from the USB IDs.
			// The kernel creates these symlinks automatically.
			if p.SerialNumber != "" {
				// Convention: /dev/serial/by-id/usb-{Manufacturer}_{Product}_{SerialNumber}-if00-port0
				// We store what we can reconstruct; the actual symlink on disk
				// is more reliable than trying to reconstruct it.
			}
		}
		result = append(result, info)
	}
	return result, nil
}

func containsI(s, substr string) bool {
	return len(s) >= len(substr) && containsFold(s, substr)
}

func containsFold(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := s[i+j]
			tc := substr[j]
			if sc != tc {
				if sc >= 'A' && sc <= 'Z' {
					sc += 32
				}
				if tc >= 'A' && tc <= 'Z' {
					tc += 32
				}
				if sc != tc {
					match = false
					break
				}
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Config holds serial port parameters for the read loop.
type Config struct {
	Port         string
	BaudRate     int
	ReadTimeout  time.Duration
	ReadSize     int
	PollFrame    []byte
	PollInterval time.Duration
	AssertDTR    bool
	AssertRTS    bool
}

// Normalized returns a copy with zero/empty fields filled by defaults.
func (c Config) Normalized() Config {
	out := c
	if out.BaudRate <= 0 {
		out.BaudRate = 115200
	}
	if out.ReadTimeout <= 0 {
		out.ReadTimeout = 250 * time.Millisecond
	}
	if out.ReadSize <= 0 {
		out.ReadSize = 512
	}
	if len(out.PollFrame) > 0 && out.PollInterval <= 0 {
		out.PollInterval = 500 * time.Millisecond
	}
	return out
}

// ReadLoop opens the serial port and streams raw byte chunks to onChunk.
// It returns nil when ctx is canceled, or an error on non-timeout read failures.
// Between reconnection attempts, the caller is responsible for backoff.
func ReadLoop(ctx context.Context, opener PortOpener, cfg Config, onChunk func([]byte)) error {
	cfg = cfg.Normalized()
	if cfg.Port == "" {
		return fmt.Errorf("serial port path is required")
	}

	port, err := opener.Open(cfg.Port, cfg.BaudRate)
	if err != nil {
		return fmt.Errorf("open serial %s: %w", cfg.Port, err)
	}
	defer port.Close()

	if err := port.SetReadTimeout(cfg.ReadTimeout); err != nil {
		return fmt.Errorf("set read timeout: %w", err)
	}
	if cfg.AssertDTR {
		if err := port.SetDTR(true); err != nil {
			return fmt.Errorf("set DTR: %w", err)
		}
	}
	if cfg.AssertRTS {
		if err := port.SetRTS(true); err != nil {
			return fmt.Errorf("set RTS: %w", err)
		}
	}

	var nextPoll time.Time
	if len(cfg.PollFrame) > 0 {
		nextPoll = time.Now()
	}

	buf := make([]byte, cfg.ReadSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if !nextPoll.IsZero() && !time.Now().Before(nextPoll) {
			if _, err := port.Write(cfg.PollFrame); err != nil {
				return fmt.Errorf("serial write poll: %w", err)
			}
			nextPoll = time.Now().Add(cfg.PollInterval)
		}

		n, err := port.Read(buf)
		if n > 0 {
			// Copy the chunk so the caller can hold it past the next read.
			chunk := append([]byte(nil), buf[:n]...)
			onChunk(chunk)
		}
		if err != nil {
			if err == io.EOF {
				continue
			}
			return fmt.Errorf("serial read: %w", err)
		}
	}
}
