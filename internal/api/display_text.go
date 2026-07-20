package api

import "time"

// DisplayTextSpan identifies a contiguous highlighted range on one LCD row.
// StartColumn is inclusive and EndColumn is exclusive.
type DisplayTextSpan struct {
	Row         int    `json:"row"`
	StartColumn int    `json:"startColumn"`
	EndColumn   int    `json:"endColumn"`
	Text        string `json:"text"`
}

// DisplayText is the screen-reader-friendly view of the current LCD state.
// Rows preserve the physical 8x40 geometry; ScreenText is the existing trimmed
// frame decoder output retained as a fallback for clients.
type DisplayText struct {
	Rows             []string          `json:"rows"`
	HighlightedSpans []DisplayTextSpan `json:"highlightedSpans"`
	SelectedText     string            `json:"selectedText"`
	Sequence         uint64            `json:"sequence"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	Source           string            `json:"source,omitempty"`
	ModelName        string            `json:"modelName,omitempty"`
	ScreenText       string            `json:"screenText,omitempty"`
}
