package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
)

func TestDisplayTextFromSnapshotPreservesGeometryAndHighlights(t *testing.T) {
	state := display.NewState()
	state.SetRow(2, "FAN MODE NORMAL")
	for col := 9; col < 15; col++ {
		state.SetAttr(2, col, 0x01)
	}
	state.Chars[3][0] = 0x80  // custom glyph: no guessed textual meaning
	state.Chars[3][1] = 0x61  // blank ROM slot: must not become ASCII text
	state.Chars[3][2] = 0x5f  // blank boundary slot: must not become DEL
	state.SetAttr(3, 0, 0x80) // alternate glyph bank, not a highlight
	state.Chars[3][3] = 0x41  // alternate bank resolves to ROM 0x21 ('A')
	state.SetAttr(3, 3, 0x80)
	updatedAt := time.Date(2026, time.July, 20, 13, 45, 0, 0, time.UTC)

	got := displayTextFromSnapshot(runtime.Snapshot{
		State:     state,
		Frame:     api.FrameInfo{ScreenText: "FAN MODE NORMAL"},
		Source:    "serial:/dev/ttyUSB0",
		Sequence:  42,
		UpdatedAt: updatedAt,
	}, "EXPERT 2K-FA")

	if len(got.Rows) != display.Rows {
		t.Fatalf("rows = %d, want %d", len(got.Rows), display.Rows)
	}
	for row, text := range got.Rows {
		if len([]rune(text)) != display.Cols {
			t.Fatalf("row %d columns = %d, want %d", row, len([]rune(text)), display.Cols)
		}
	}
	if got.Rows[2] != "FAN MODE NORMAL"+strings.Repeat(" ", 25) {
		t.Fatalf("row 2 = %q", got.Rows[2])
	}
	if got.Rows[3][:3] != "   " {
		t.Fatalf("non-text ROM slots decoded as %q, want blanks", got.Rows[3][:3])
	}
	if got.Rows[3][3] != 'A' {
		t.Fatalf("alternate-bank glyph decoded as %q, want A", got.Rows[3][3])
	}
	if len(got.HighlightedSpans) != 1 {
		t.Fatalf("highlighted spans = %+v, want one", got.HighlightedSpans)
	}
	span := got.HighlightedSpans[0]
	if span.Row != 2 || span.StartColumn != 9 || span.EndColumn != 15 || span.Text != "NORMAL" {
		t.Fatalf("highlighted span = %+v", span)
	}
	if got.SelectedText != "NORMAL" || got.Sequence != 42 || got.UpdatedAt != updatedAt || got.Source != "serial:/dev/ttyUSB0" || got.ModelName != "EXPERT 2K-FA" || got.ScreenText != "FAN MODE NORMAL" {
		t.Fatalf("unexpected context: %+v", got)
	}
}

func TestDisplayTextEndpointReturnsRuntimeEnvelope(t *testing.T) {
	state := display.NewState()
	state.SetRow(0, "SET MENU")
	for col := 0; col < 3; col++ {
		state.SetAttr(0, col, 0x01)
	}
	updatedAt := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	store := runtime.NewStore(runtime.Snapshot{
		State:     state,
		Telemetry: api.Telemetry{ModelName: "EXPERT 2K-FA"},
		Frame:     api.FrameInfo{ScreenText: "SET MENU"},
		Source:    "fixture:set-menu",
		Sequence:  7,
		UpdatedAt: updatedAt,
	})
	handler := newTestHandler(store, runtime.FixtureCatalog{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/display/text", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Success bool            `json:"success"`
		Data    api.DisplayText `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || len(body.Data.Rows) != 8 || body.Data.Sequence != 7 || body.Data.SelectedText != "SET" || body.Data.ModelName != "EXPERT 2K-FA" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
}

func TestDisplayTextEndpointRejectsOtherMethodsWithAPIEnvelope(t *testing.T) {
	handler := newTestHandler(runtime.NewStore(runtime.Snapshot{}), runtime.FixtureCatalog{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/display/text", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	var body api.Response
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Error == "" {
		t.Fatalf("unexpected body: %+v", body)
	}
}
