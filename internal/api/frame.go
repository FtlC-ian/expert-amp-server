package api

type FrameInfo struct {
	Source      string `json:"source"`
	Length      int    `json:"length"`
	StartOffset int    `json:"startOffset"`
	ScreenText  string `json:"screenText,omitempty"`
}
