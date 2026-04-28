package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestStreamDecoderExtractsSingleFrame(t *testing.T) {
	// Build a frame: prefix + some body + another prefix as boundary.
	frame := make([]byte, 0, 128)
	frame = append(frame, RadioDisplayPrefix...)
	// 100 bytes of body
	for i := 0; i < 100; i++ {
		frame = append(frame, 0x20) // spaces
	}
	// Closing boundary
	frame = append(frame, RadioDisplayPrefix...)

	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{})
	got := decoder.Push(frame)
	if len(got) != 1 {
		t.Fatalf("frames = %d, want 1", len(got))
	}
	if len(got[0]) != len(RadioDisplayPrefix)+100 {
		t.Fatalf("frame length = %d, want %d", len(got[0]), len(RadioDisplayPrefix)+100)
	}
	if !bytes.HasPrefix(got[0], RadioDisplayPrefix) {
		t.Fatal("frame does not start with radio display prefix")
	}
}

func TestStreamDecoderExtractsMultipleFramesFromSingleChunk(t *testing.T) {
	buildFrame := func(bodyLen int) []byte {
		f := make([]byte, 0, len(RadioDisplayPrefix)+bodyLen+len(RadioDisplayPrefix))
		f = append(f, RadioDisplayPrefix...)
		for i := 0; i < bodyLen; i++ {
			f = append(f, 0x20)
		}
		f = append(f, RadioDisplayPrefix...)
		return f
	}

	data := append(buildFrame(80), buildFrame(100)...)

	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{})
	got := decoder.Push(data)
	if len(got) != 2 {
		t.Fatalf("frames = %d, want 2", len(got))
	}
	if len(got[0]) != len(RadioDisplayPrefix)+80 {
		t.Fatalf("frame 0 length = %d, want %d", len(got[0]), len(RadioDisplayPrefix)+80)
	}
	if len(got[1]) != len(RadioDisplayPrefix)+100 {
		t.Fatalf("frame 1 length = %d, want %d", len(got[1]), len(RadioDisplayPrefix)+100)
	}
}

func TestStreamDecoderAssemblesSplitChunks(t *testing.T) {
	// Build one complete frame then split it across two Push calls.
	frame := make([]byte, 0, 128)
	frame = append(frame, RadioDisplayPrefix...)
	for i := 0; i < 100; i++ {
		frame = append(frame, 0x20)
	}
	frame = append(frame, RadioDisplayPrefix...)

	mid := len(frame) / 2
	chunk1 := frame[:mid]
	chunk2 := frame[mid:]

	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{})
	frames1 := decoder.Push(chunk1)
	if len(frames1) != 0 {
		t.Fatalf("partial push gave %d frames, want 0", len(frames1))
	}
	frames2 := decoder.Push(chunk2)
	if len(frames2) != 1 {
		t.Fatalf("after second push, frames = %d, want 1", len(frames2))
	}
}

func TestStreamDecoderSkipsShortFrames(t *testing.T) {
	// A frame shorter than minFrameLen should be skipped.
	frame := make([]byte, 0)
	frame = append(frame, RadioDisplayPrefix...)
	frame = append(frame, 0x20) // only 1 byte body
	frame = append(frame, RadioDisplayPrefix...)

	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{MinFrameLen: 64})
	got := decoder.Push(frame)
	if len(got) != 0 {
		t.Fatalf("frames = %d, want 0 (frame too short)", len(got))
	}
}

func TestStreamDecoderIgnoresGarbageBeforePrefix(t *testing.T) {
	garbage := make([]byte, 50)
	for i := range garbage {
		garbage[i] = 0xFF
	}

	frame := make([]byte, 0, 128)
	frame = append(frame, RadioDisplayPrefix...)
	for i := 0; i < 80; i++ {
		frame = append(frame, 0x20)
	}
	frame = append(frame, RadioDisplayPrefix...)

	data := append(garbage, frame...)

	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{})
	got := decoder.Push(data)
	if len(got) != 1 {
		t.Fatalf("frames = %d, want 1", len(got))
	}
}

func TestStreamDecoderCapsBufferOnPartialPrefix(t *testing.T) {
	// Feed data that starts with a prefix but has no closing boundary,
	// ensuring the buffer doesn't exceed maxBuffer.
	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{MaxBuffer: 256})

	// First push: prefix + body
	chunk := append([]byte{}, RadioDisplayPrefix...)
	for i := 0; i < 300; i++ {
		chunk = append(chunk, 0x20)
	}
	got := decoder.Push(chunk)
	if len(got) != 0 {
		t.Fatalf("frames = %d, want 0 (no closing boundary)", len(got))
	}

	// Push closing boundary
	got = decoder.Push(RadioDisplayPrefix)
	// After trimming, the frame might be truncated but should still produce a frame
	// because we have prefix + body (capped at 256) + prefix.
	if len(got) != 1 {
		t.Fatalf("frames = %d, want 1 after closing boundary", len(got))
	}
}

func TestStreamDecoderSplitsGetLCDBeforeTrailingStatusFrame(t *testing.T) {
	lcd := loadFixture(t, "real_home_status_frame.bin")
	status := []byte("\xaa\xaa\xaaC" + strings.Repeat("S", 72))
	stream := append(append([]byte{}, lcd...), status...)
	stream = append(stream, lcd...)

	decoder := NewDisplayStreamDecoder(StreamDecoderConfig{})
	got := decoder.Push(stream)
	if len(got) != 2 {
		t.Fatalf("frames = %d, want 2", len(got))
	}
	if len(got[0]) != getLCDTotalLen || len(got[1]) != getLCDTotalLen {
		t.Fatalf("frame lengths = %d, %d; want %d, %d", len(got[0]), len(got[1]), getLCDTotalLen, getLCDTotalLen)
	}
	if !bytes.Equal(got[0], lcd) || !bytes.Equal(got[1], lcd) {
		t.Fatal("decoder did not preserve exact GetLCD frames")
	}
}
