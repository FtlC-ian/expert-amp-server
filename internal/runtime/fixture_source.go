package runtime

import (
	"context"
	"fmt"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

type FixtureCatalog struct {
	States map[string]display.State
	Frames map[string]api.FrameInfo
}

func (c FixtureCatalog) State(kind string) (display.State, bool) {
	state, ok := c.States[kind]
	return state, ok
}

func (c FixtureCatalog) Frame(kind string) (api.FrameInfo, bool) {
	frame, ok := c.Frames[kind]
	return frame, ok
}

type FixtureSource struct {
	Catalog   FixtureCatalog
	Kind      string
	Telemetry api.Telemetry
}

func (s FixtureSource) Poll(ctx context.Context) (Update, error) {
	kind := s.Kind
	if kind == "" {
		kind = "home"
	}
	state, ok := s.Catalog.States[kind]
	if !ok {
		return Update{}, fmt.Errorf("fixture state %q not found", kind)
	}
	frame, ok := s.Catalog.Frames[kind]
	if !ok {
		frame = api.FrameInfo{Source: "fixture:" + kind}
	}
	telemetry := s.Telemetry
	if telemetry.Source == "" {
		telemetry.Source = "fixture:" + kind
	}
	return Update{
		State:     state,
		Telemetry: telemetry,
		Frame:     frame,
		FrameKind: kind,
		Source:    telemetry.Source,
	}, nil
}
