# Display accessibility and passive capture

Expert Amp Server exposes the current LCD as text without requiring a screenshot or visual transcription. This is the foundation for screen-reader clients and for model-specific menu recognition.

## Accessible text endpoint

`GET /api/v1/display/text` returns the standard `{ "success": true, "data": ... }` envelope. Its data includes:

- `rows`: exactly eight strings of exactly 40 characters, preserving the physical LCD geometry
- `highlightedSpans`: contiguous reverse-video ranges with zero-based row and column coordinates
- `selectedText`: non-blank highlighted text in row-major order
- `sequence` and `updatedAt`: the authoritative runtime display identity and timestamp
- `source` and `modelName`: capture context when available
- `screenText`: the existing trimmed decoder output as a compatibility fallback

Unknown or custom glyphs decode as spaces. Their byte values are not lost: clients that need glyph-level evidence can read the exact `chars` and `attrs` arrays from `GET /api/v1/display/state`.

The endpoint describes the current display. It does not recognize a menu page, assign meaning to custom symbols, or send amplifier controls.

## Passive display recorder

`display-capture` polls only `GET /api/v1/version` and `GET /api/v1/runtime/snapshot`. It never calls an action or settings endpoint.

Build it with:

```bash
go build -o display-capture ./cmd/display-capture
```

Record new display states until interrupted:

```bash
./display-capture \
  -base-url http://amp.local:8088 \
  -output 2k-fa-display-captures.ndjson
```

Record one bounded observation with an operator-supplied transition label:

```bash
./display-capture \
  -base-url http://amp.local:8088 \
  -output 2k-fa-display-captures.ndjson \
  -once \
  -transition-label "pressed SET"
```

Use `-max-unique N` to stop after recording `N` previously unseen states. `-interval` controls the polling interval and defaults to one second.

The output is append-only NDJSON with one JSON object per first-seen exact state. Each record includes:

- a deterministic SHA-256 `stateHash` and `previousStateHash`
- the optional `transitionLabel`
- the exact 8x40 `chars` and `attrs` arrays
- decoded `screenText`, LCD flags, and complete frame metadata
- runtime sequence, source, frame kind, and timestamps
- server build metadata and amplifier telemetry/model context

The hash covers characters, attributes, and LCD flags, but not timestamps or sequence numbers. The recorder scans an existing output file on startup, so it can resume without duplicating states already captured. Transition links are based only on states observed during the current run: if a previously captured screen is revisited, it becomes the transition source for the next new state, but the recorder never invents a transition from an old file's final line.

Capture files are created with owner-only permissions. Keep the raw NDJSON as evidence: recognizers should be derived from it without rewriting the original observations.

## Suggested 2K-FA capture session

1. Record the home screen once.
2. Start the passive recorder before entering Set mode.
3. Navigate only through the known manual-derived menu path, without changing values.
4. Stop after one observation of each menu item at its current value.
5. Compare a small sample against the physical LCD to validate decoded text and highlight placement.

Do not automate traversal from captures alone. A future model-specific recognizer must reject unknown screens, and any traversal state machine must abort on a sequence timeout or unexpected state.
