# Expert Amp Server — Architecture

This document describes the local server's current shape: what packages exist, what they do, how data flows, and what is wired versus stubbed.

Read this before adding a new package, endpoint, or layer.

---

## What this is

Expert Amp Server is a small local service that runs near an SPE Expert amplifier — ideally on a Raspberry Pi on the same LAN as the amp. It does not require an internet connection.

The server has two jobs:

1. **Ingest binary display frames** from the amp's serial/USB connection and decode them into a structured screen model.
2. **Expose that state** through a local web UI and a REST API for use by Node-RED, Thetis gauges, scripts, and AI agents.
3. **Prefer protocol-native status** for machine-readable amp state while keeping display-derived data for mirroring and fallback.

The current service can run from fixture/demo state for development, but the production path is a live serial source configured through the local settings/config file. Live ingest owns display-frame refresh, protocol status polling, button writes, and the shared in-memory snapshot served by the UI and `/api/v1/...` endpoints.

---

## Repository layout

```
cmd/
  server/       — HTTP server binary (main.go + embedded index.html)
  render-preview/ — standalone PNG render tool for dev/debug

internal/
  protocol/     — binary display-frame decoding plus documented status-poll parsing
  display/      — the 8×40 display state model and cell diff
  font/         — SPE-style LCD font table used by the renderer
  render/       — pixel (PNG) and SVG renderers
  api/          — shared JSON types (telemetry, button action, frame info)
  web/          — alternate embedded UI (not currently used by the server binary)

fixtures/
  real_home_status_frame.bin  — captured home/status screen
  real_menu_frame.bin         — captured menu screen
  real_panel_frame.bin        — captured panel screen
  sample_display_frame.bin    — another captured frame (same header as home)

docs/
  ROADMAP.md
  ARCHITECTURE.md  ← this file
  PROTOCOL.md
```

---

## Package responsibilities

### `internal/protocol`

Handles raw display frames and documented status-poll responses from the amp.

- `IsRadioDisplayFrame` — checks whether a frame starts with the known 8-byte display header (`AA AA AA 6A 01 95 FE 01`)
- `DecodeDisplayChar` — maps a single byte to a printable character or a placeholder glyph
- `DisplayBodyOffset` — fixed protocol body offset used for display decoding
- `GuessDisplayStart` — diagnostic heuristic retained for fixture/frame inspection
- `StateFromFrame` — ties those together: validates header → decodes 8×40 chars into a `display.State`
- `ScreenText` — extracts a plain-text representation from a frame (trims trailing spaces and empty rows)
- `LoadFixtureState` — loads a `.bin` file from disk, decodes it, and returns both the state and metadata
- `StatusFromFrame` / `StatusFromResponse` — parse the documented status-poll response into protocol-native `api.Status` fields

This package owns the boundary between "raw bytes from hardware" and "structured display model."

### `internal/display`

Defines the display model and operations on it.

- `State` — an 8×40 grid of character bytes (`Chars[row][col]`) plus 40 attribute bytes (`Attrs[col]`), one per column
- `NewState` — creates an empty state (fills chars with `0x60`, which renders as a space)
- `DemoState` / `DemoStateAlt` — hard-coded demo screens for development use
- `Compare` — cell-level diff between two states, used by `/diff` and diagnostics

The display model is the canonical in-process representation of "what the amp's screen currently shows." It is not the canonical machine-readable status contract when protocol-native status-poll data is available.

### `internal/font`

Manages the font ROM used for pixel rendering.

- `ROM` — a 256-glyph table, 8 bytes per glyph (one byte per row, MSB = leftmost pixel)
- `Builtin()` — returns the 256-glyph SPE Expert 1.3K display ROM table used by the renderer
- `Glyph(code, attr)` — looks up one glyph, applying attribute effects:
  - `attr & 0x80` nonzero → alternate glyph bank (subtracts 0x20 from the char code index)
  - `attr & 0x7f` nonzero → invert all pixel bits (highlight / reverse video)

The current font table is derived from the SPE Expert 1.3K display tooling and is kept in source form so rendered LCD screenshots match the real amp closely. This is useful for validation, but it is also a provenance item to review before a broader public release. Do not add vendor executables or raw binary blobs to the repo.

### `internal/render`

Two renderers, both stateless functions that take a `display.State` and a `*font.ROM`:

- `Image(state, rom, fg, bg)` → `*image.Gray` — rasterizes the display to a grayscale bitmap at 8 pixels per cell (320×64 pixels total)
- `SVG(state, rom, fg, bg, scale)` → `string` — emits inline SVG with one `<g>` per cell, suitable for partial DOM updates

Both walk the 8×40 cell grid, call `rom.Glyph` per cell, and paint pixels or emit rectangles.

### `internal/api`

Shared JSON types:

- `ButtonAction{Name string}` — payload for `POST /api/actions/button`
- `Telemetry{...}` — the current runtime telemetry snapshot shape, still used for UI and fallback data
- `Status{Telemetry + BandCode/BandText, RXAntenna, WarningCode/AlarmCode, WarningsText/AlarmsText}` — the canonical automation status surface, preferring protocol-native status poll data and filling gaps from telemetry fallback when needed
- `FrameInfo{Source, Length, StartOffset, ScreenText}` — frame decode metadata returned by `/api/frame`

These types are defined centrally so the server handlers, runtime, and transports share one schema.

### `cmd/server`

The HTTP server wires everything together. It:

1. Builds the font ROM once at startup
2. Loads fixture files for development/fallback state
3. Starts a live serial source when a serial port is configured
4. Registers handlers for every endpoint
5. Serves an embedded `index.html` from the binary

**Current endpoint map:**

| Endpoint | Method | Description | Status |
|---|---|---|---|
| `/` | GET | Embedded web UI | Live (demo) |
| `/healthz` | GET | Plain process liveness check with version header | Live |
| `/api/v1/version` | GET | Canonical build/version metadata JSON | Live |
| `/api/v1/display/state` | GET | Canonical display state JSON | Live |
| `/api/v1/display/frame` | GET | Canonical frame metadata JSON | Live |
| `/api/v1/display/render.png` | GET | Canonical rendered PNG | Live |
| `/api/v1/display/render.svg` | GET | Canonical rendered SVG | Live |
| `/api/v1/display/ws` | GET | Canonical display refresh websocket event stream | Live |
| `/api/v1/status` | GET | Canonical amp status JSON | Live, prefers protocol-native status poll data |
| `/api/v1/status/ws` | GET | Canonical amp status websocket | Live |
| `/api/v1/telemetry` | GET | Canonical telemetry snapshot route | Live runtime snapshot, mainly UI/fallback |
| `/api/v1/alarms` | GET | Canonical alarms route | Live phase-1 stub |
| `/api/v1/runtime` | GET | Canonical runtime settings/status view | Live |
| `/api/v1/runtime/snapshot` | GET | Canonical runtime snapshot route | Live |
| `/api/v1/runtime/ingest` | GET | Canonical ingest diagnostics route | Live |
| `/api/v1/runtime/restart` | POST | Canonical restart request route | Live when restart support is configured |
| `/api/v1/serial-ports` | GET | Canonical serial-port discovery route | Live |
| `/api/v1/settings` | GET/POST | Canonical persisted local settings route | Live when config support is configured |
| `/api/v1/actions/button` | POST | Canonical documented front-panel action route | Live |
| `/api/v1/actions/wake` | POST | Experimental serial DTR/RTS wake route | Live |
| `/api/v1/openapi.json` | GET | Canonical OpenAPI artifact | Live |
| `/api/v1/docs` | GET | Canonical local docs UI | Live |
| `/state`, `/api/status`, `/api/telemetry`, `/api/frame`, `/api/runtime`, `/api/runtime/snapshot`, `/api/runtime/ingest`, `/api/actions/button`, `/render.png`, `/render.svg` | various | Legacy compatibility aliases/holdovers for older clients | Live, but not preferred |
| `/diff` | GET | Legacy debug/demo helper | Live, no canonical v1 alias |

---

## Data flow (current)

Live mode:

```
SPE serial/USB device
      │
      ▼
runtime.SerialSource
      │
      ├── display frames ──► protocol.StateFromFrame ──► display.State/runtime snapshot
      │                                           │
      │                                           └──► render.Image / render.SVG / display websocket invalidation
      │
      └── status poll responses ──► protocol.StatusFromFrame ──► runtime.StatusState
                                                        │
                                                        └──► /api/v1/status and /api/v1/status/ws
```

Development/fallback mode:

```
fixtures/*.bin ──► protocol.LoadFixtureState ──► display.State/runtime snapshot
```

`display.State` remains the pivot for screen mirroring and rendered LCD output. `StatusState` is the pivot for machine-readable amp status: it prefers protocol-native status-poll data and fills only the remaining gaps from the current runtime/display snapshot.

---

## What is wired vs. what is a stub

| Capability | Status |
|---|---|
| Binary frame decode (fixtures) | Working |
| Rendered PNG and SVG | Working |
| Cell diff | Working |
| Runtime telemetry snapshot | Working |
| Canonical status surface (`/api/v1/status`) | Working, prefers protocol-native status poll data with telemetry fallback |
| Live serial ingest | Working when a serial port is configured; fixture mode remains available for development |
| Display-derived telemetry extraction | Working in a conservative phase-1 form |
| Button transport to hardware | Working when a live serial/button transport is configured; otherwise returns unavailable cleanly |
| WebSocket status feed (`/api/v1/status/ws`) | Working, event-driven fanout from the authoritative shared status state used by `GET /api/v1/status` |
| Display refresh websocket (`/api/v1/display/ws`) | Working, pushes lightweight snapshot-sequence events from the shared runtime store so image clients can refresh only on real display changes |
| General WebSocket / SSE for other live updates | Status and display websocket paths are working; no broad event bus beyond those |
| OpenAPI spec (`/api/v1/openapi.json`) | Working, served by the app as a conservative phase 1 artifact |
| Local docs UI (`/api/v1/docs`) | Working, renders the served OpenAPI document into a built-in local reference page |

---

## Design constraints

- **Trusted-LAN station appliance.** This service is meant to run near the amp and be reached from operator machines on the same station LAN. It is not an internet-facing web app and should sit behind the user's LAN, VPN, firewall, or reverse proxy for any remote use.
- **Captured data beats theory.** If real frames disagree with assumptions, update the code and document the difference.
- **No vendor binaries.** The repo must not include vendor executables. Protocol/font provenance needs to be documented honestly before public release.
- **API must be boring and stable.** Prefer explicit JSON over cleverness. The canonical REST surface is the current `/api/v1/...` API; older non-v1 routes are compatibility holdovers, not the preferred contract.
- **Every feature needs tests.** Decoder changes need regression tests against fixture files.
