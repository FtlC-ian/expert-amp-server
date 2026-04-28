package protocol

import "bytes"

const (
	defaultMinFrameLen = 64
	defaultMaxBuffer   = 8192
)

// DisplayStreamDecoder reconstructs display frames from a raw serial byte
// stream. It uses the known RadioDisplayPrefix as a boundary marker: when the
// prefix appears again, the bytes between the previous prefix and the new one
// form a candidate frame.
type DisplayStreamDecoder struct {
	minFrameLen int
	maxBuffer   int
	buf         []byte
}

// StreamDecoderConfig tunes the stream decoder's frame extraction.
type StreamDecoderConfig struct {
	MinFrameLen int `json:"minFrameLen"`
	MaxBuffer   int `json:"maxBuffer"`
}

// NewDisplayStreamDecoder creates a decoder with the given config, filling
// defaults for zero fields.
func NewDisplayStreamDecoder(cfg StreamDecoderConfig) *DisplayStreamDecoder {
	if cfg.MinFrameLen <= 0 {
		cfg.MinFrameLen = defaultMinFrameLen
	}
	if cfg.MaxBuffer <= 0 {
		cfg.MaxBuffer = defaultMaxBuffer
	}
	return &DisplayStreamDecoder{
		minFrameLen: cfg.MinFrameLen,
		maxBuffer:   cfg.MaxBuffer,
		buf:         make([]byte, 0, cfg.MaxBuffer),
	}
}

// Push appends a chunk of raw serial bytes and returns any complete frames
// extracted from the internal buffer. Each returned frame is a copy safe for
// the caller to retain.
func (d *DisplayStreamDecoder) Push(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	d.buf = append(d.buf, chunk...)

	var frames [][]byte
	for {
		start := bytes.Index(d.buf, RadioDisplayPrefix)
		if start < 0 {
			d.trimNoPrefix()
			break
		}
		if start > 0 {
			d.buf = d.buf[start:]
		}

		if IsGetLCDResponseFrame(d.buf) {
			frame := append([]byte(nil), d.buf[:getLCDTotalLen]...)
			frames = append(frames, frame)
			d.buf = d.buf[getLCDTotalLen:]
			continue
		}

		next := bytes.Index(d.buf[len(RadioDisplayPrefix):], RadioDisplayPrefix)
		if next < 0 {
			d.trimWithPrefix()
			break
		}
		end := len(RadioDisplayPrefix) + next
		frame := append([]byte(nil), d.buf[:end]...)
		if len(frame) >= d.minFrameLen {
			frames = append(frames, frame)
		}
		d.buf = d.buf[end:]
	}
	return frames
}

// trimNoPrefix keeps the tail of the buffer that could be the start of a
// prefix so we don't miss a split-across-chunks boundary.
func (d *DisplayStreamDecoder) trimNoPrefix() {
	keep := len(RadioDisplayPrefix) - 1
	if keep < 0 {
		keep = 0
	}
	if len(d.buf) <= keep {
		return
	}
	d.buf = append([]byte(nil), d.buf[len(d.buf)-keep:]...)
}

// trimWithPrefix caps the buffer size when we have a prefix but no closing
// prefix yet, preventing unbounded growth from a noisy stream.
func (d *DisplayStreamDecoder) trimWithPrefix() {
	if len(d.buf) <= d.maxBuffer {
		return
	}
	d.buf = d.buf[:d.maxBuffer]
}
