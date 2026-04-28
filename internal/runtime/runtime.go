package runtime

import (
	"context"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
)

type Snapshot struct {
	State     display.State `json:"state"`
	Telemetry api.Telemetry `json:"telemetry"`
	Frame     api.FrameInfo `json:"frame"`
	FrameKind string        `json:"frameKind,omitempty"`
	Source    string        `json:"source,omitempty"`
	Sequence  uint64        `json:"sequence"`
	UpdatedAt time.Time     `json:"updatedAt,omitempty"`
}

type Update struct {
	State     display.State
	Telemetry api.Telemetry
	Frame     api.FrameInfo
	FrameKind string
	Source    string
}

type Source interface {
	Poll(ctx context.Context) (Update, error)
}

type StaticSource struct {
	Update Update
}

func (s StaticSource) Poll(context.Context) (Update, error) {
	return s.Update, nil
}

type Store struct {
	mu          sync.RWMutex
	snapshot    Snapshot
	subscribers map[chan Snapshot]struct{}
}

func NewStore(initial Snapshot) *Store {
	return &Store{
		snapshot:    initial,
		subscribers: make(map[chan Snapshot]struct{}),
	}
}

func (s *Store) Current() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Store) Subscribe(buffer int) (<-chan Snapshot, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Snapshot, buffer)

	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = make(map[chan Snapshot]struct{})
	}
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[ch]; !ok {
			return
		}
		delete(s.subscribers, ch)
		close(ch)
	}

	return ch, unsubscribe
}

func (s *Store) Apply(update Update) (Snapshot, bool) {
	s.mu.Lock()

	resolvedSource := update.Source
	if resolvedSource == "" {
		resolvedSource = update.Telemetry.Source
	}
	if resolvedSource == "" {
		resolvedSource = update.Frame.Source
	}

	if s.snapshot.State == update.State &&
		reflect.DeepEqual(s.snapshot.Telemetry, update.Telemetry) &&
		reflect.DeepEqual(s.snapshot.Frame, update.Frame) &&
		s.snapshot.FrameKind == update.FrameKind &&
		s.snapshot.Source == resolvedSource {
		snapshot := s.snapshot
		s.mu.Unlock()
		return snapshot, false
	}

	next := Snapshot{
		State:     update.State,
		Telemetry: update.Telemetry,
		Frame:     update.Frame,
		FrameKind: update.FrameKind,
		Source:    resolvedSource,
		Sequence:  s.snapshot.Sequence + 1,
		UpdatedAt: time.Now().UTC(),
	}
	s.snapshot = next
	subscribers := make([]chan Snapshot, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()

	for _, ch := range subscribers {
		pushSnapshot(ch, next)
	}
	return next, true
}

func pushSnapshot(ch chan Snapshot, snapshot Snapshot) {
	select {
	case ch <- snapshot:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- snapshot:
	default:
	}
}

type Poller struct {
	Source   Source
	Store    *Store
	Interval time.Duration
	Logger   *log.Logger
	// Enabled is consulted before every poll tick. When set and returning false,
	// the tick is skipped but the poller keeps running so it resumes immediately
	// when re-enabled. When nil, polling is always active.
	Enabled func() bool
}

func (p *Poller) Run(ctx context.Context) error {
	if p.Source == nil {
		return nil
	}
	if p.Store == nil {
		return nil
	}
	if p.Interval <= 0 {
		p.Interval = 200 * time.Millisecond
	}

	if p.Enabled == nil || p.Enabled() {
		p.pollOnce(ctx)
	}

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if p.Enabled == nil || p.Enabled() {
				p.pollOnce(ctx)
			}
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	update, err := p.Source.Poll(ctx)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Printf("snapshot poll failed: %v", err)
		}
		return
	}
	_, _ = p.Store.Apply(update)
}
