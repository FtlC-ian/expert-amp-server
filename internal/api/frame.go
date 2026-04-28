package api

type FrameInfo struct {
	Source      string    `json:"source"`
	Length      int       `json:"length"`
	StartOffset int       `json:"startOffset"`
	ScreenText  string    `json:"screenText,omitempty"`
	LCDFlags    *LCDFlags `json:"lcdFlags,omitempty"`
}

// LCDFlags exposes the GetLCD/LCD-response flag word. The amp sends these two
// bytes inverted and little-endian immediately before the 360-byte LCD payload.
// Decoded is RawInverted XOR 0xffff. When the checksum is valid, the decoded
// high bits are also promoted as front-panel LED states for the amp-shaped UI.
type LCDFlags struct {
	RawInverted     uint16   `json:"rawInverted"`
	Decoded         uint16   `json:"decoded"`
	ChecksumPresent bool     `json:"checksumPresent"`
	ChecksumValid   bool     `json:"checksumValid"`
	LEDs            *LCDLEDs `json:"leds,omitempty"`
}

// LCDLEDs are front-panel LED states decoded from checksum-valid LCD flags.
// Operate, set/menu, and tune were validated on a live Expert 1.3K-FA. TX uses
// the adjacent decoded bit reported by the LCD flag map and should still be
// confirmed with a safe live TX capture.
type LCDLEDs struct {
	TX      bool `json:"tx"`
	Operate bool `json:"operate"`
	Set     bool `json:"set"`
	Tune    bool `json:"tune"`
}
