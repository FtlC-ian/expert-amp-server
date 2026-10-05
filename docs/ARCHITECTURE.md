# Expert Amp Server — Architecture

This document describes the local server's current shape: what packages exist, what they do, how data flows, and what is wired versus stubbed.

Read this before adding a new package, endpoint, or layer.

---

## What this is

Expert Amp Server is a small local service that runs near an SPE Expert amplifier — ideally on a Raspberry Pi on the same LAN as the amp. It does not require an internet connection.

The server has two jobs:

1. **Ingest binary display frames** from the amp's serial/USB connection and decode them into a structured screen model.
2. **Expose that state** through a local web UI and a REST API for use by Node-RED, station dashboards, and scripts.
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
  menudebug/    — guarded menu discovery sessions and sanitized report generation
  rawpassthrough/ — exclusive raw serial-over-TCP lease for a single external client

fixtures/
  real_home_status_frame.bin  — captured home/status screen
  real_menu_frame.bin         — captured menu screen
  real_panel_frame.bin        — captured panel screen
  sample_display_frame.bin    — another captured frame (same header as home)

docs/
  ARCHITECTURE.md  ← this file
  PROTOCOL.md
  INSTALL_PI.md
  integrations/
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
- `StatusFromFrame` / `StatusFromResponse` — parse the documented status-poll response into protocol-native `api.Status` fields, converting unitless amplifier readings into canonical Celsius when configured

This package owns the boundary between "raw bytes from hardware" and "structured display model."

The vendor status response does not identify whether temperature numbers are Celsius or Fahrenheit. `amplifierTemperatureUnit` therefore belongs at this ingest boundary: all `*C` API fields and policy inputs remain canonical Celsius, while display companions retain the configured amplifier unit.

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

The current font table is a bundled SPE-style LCD font table kept in source form so rendered LCD screenshots match the real amp closely. Do not add vendor executables or raw binary blobs to the repo.

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
- `FrameInfo{Source, Length, StartOffset, ScreenText, LCDFlags}` — frame decode metadata returned by `/api/frame`

These types are defined centrally so the server handlers, runtime, and transports share one schema.

### `internal/rawpassthrough`

Optional, disabled by default. Exposes the amplifier's serial link over TCP so a
single external client — for example SPE Expert Controller, which connects to a
"network/serial adapter" over TCP/IP — can drive the amplifier directly through
the machine already running this server, with no second cable and no separate
LAN adapter.

The amplifier tolerates one serial master at a time, so this is an **exclusive
lease, not a multiplexer**:

- With no client connected the server behaves exactly as it always has.
- When a client connects, `SerialSource.BeginRawPassthrough` stops the internal
  read loop and waits for it to unwind (the same `retireCurrentSession` handshake
  `SendWake` uses), then hands the caller its own port handle. Bytes are
  forwarded verbatim in both directions.
- On disconnect the port is closed, polling resumes automatically, and the
  next reconnect is signalled immediately.
- A second concurrent client is rejected and closed, not queued.

While the lease is held the server refuses its own writes rather than blocking
on them. `rawPassthroughActive` is read and written only under `writeMu`, and
both `SendWake` and `writeFrameForSerialSession` take `writeMu` before
`lifecycleMu`, so they fail fast with HTTP 409 instead of waiting on a lock the
session holds for as long as an external client stays connected. The session
also holds an `ActuationCoordinator` lease under
`ActuationOwnerRawPassthrough`, so button and wake callers get the usual busy
error before they reach the serial layer, and a passthrough session refuses to
start while an automatic transaction is already driving the amplifier.

**Automatic control is refused up front, not degraded.** A passthrough session
cannot start while automatic fan control or overtemperature standby is armed.
The listener asks `Config.ArmedAutomaticControls` before it takes either the
coordinator lease or the serial port, and refuses the connection by closing it
without writing a byte. The raw socket carries the amplifier's stream and
nothing else, so the refusal reason never travels on it: the client sees a
clean EOF, or `ECONNRESET` if it wrote first. The reason reaches the operator
through the server log and `GET /api/v1/raw-passthrough`, which reports it in
`blockedByArmedControls` and `note` from the same `AutomaticControlsArmedError`
that names every control to disarm. (That error belongs to the raw listener's
diagnostic path and nothing else: it is built only to write the refusal log line
and that `note`, and it is never returned from an HTTP handler, so its `HTTPStatus`
of 409 is never actually served to anyone. The 409 that button and wake callers
receive comes from their own coordinator and `rawPassthroughActive` errors,
described above, not from this one.)
Nothing is silently suspended for the duration of a lease and nothing is
automatically restored afterwards: tracking what was suspended and restoring it
across disconnects, crashes and failed port reclamation is a second state
machine, and refusal keeps the state simple and honest. An operator who chooses
passthrough does so knowing those controls are unavailable.

**Tapped telemetry is display evidence only.** The amplifier→client direction is
decoded as it is forwarded, using the same `StatusStreamDecoder` as normal
operation, so while the external client polls `0x90` the dashboard and API keep
tracking the amplifier at no extra cost on the wire. Those frames are relabelled
`provenance: "passthrough-tap"` before publication, because the amplifier
answered a question the server did not ask.

That label is the whole boundary. `StatusState.Resolve` merges tapped state for
display and the provenance travels with the merged status, while fan policy
(`fanpolicy.Evaluate`, `actionBlocks`) and overtemperature standby
(`monitoring.Controller.Observe`) each test for `"status-poll"` exactly and so
refuse it as authority. Tapped state goes stale normally when the client stops
polling, and staleness is never dressed up as freshness. There is no byte
injection, no response swallowing, and no command/reply correlation — those
would make this a protocol proxy rather than a byte-stream lease.

**The pre-lease status frame is invalidated as the lease begins.** A lease stops
the server's own polling, so `BeginRawPassthrough` calls
`StatusState.InvalidatePreLeaseStatus`: the retained `status-poll` frame stops
being canonical immediately, rather than aging out of its five-second contact
window. Without that, a client that forwards display frames but never asks for
`0x90` — which is what Expert Controller Plus actually does, in receive and
under transmit alike — would leave
`/api/v1/status` and `/api/v1/alarms` serving pre-lease temperature, SWR, TX and
output level labelled `status-poll` with `recentContact: true`, for a reading
nothing was refreshing. Canonical status falls back to display-derived state
until a tapped `0x90` or the first poll after the lease supersedes it; the
invalidation lifts itself on the next published frame, so nothing has to be
restored on disconnect. The internal gates never depended on this — they test
provenance and contact themselves — so this is an API-honesty fix, not a safety
one.

Falling back to display-derived state is not the same as reporting nothing, and
the distinction is worth being precise about. The amplifier prints temperature,
output level, SWR and TX on its own LCD, so the display tap keeps decoding them
and canonical status keeps reporting them — but as `display-frame` with
display-derived confidence, not as a status poll. What disappears is what only
the status reply carries, such as the protocol band code and text. `recentContact`
then follows the display snapshot, which advances when the decoded screen changes
rather than on every frame, so a static screen ages out of the contact window
while frames are still arriving. That is honest but pessimistic, and it is
existing display-path behavior rather than anything the invalidation introduced.

Tapped status is bounded the same way. A tapped `0x90` is canonical only while
the external client keeps asking for one; once it stops, the retained frame
expires back to display-derived state instead of outranking newer display
telemetry and borrowing its timestamp to call itself current. `status-poll` is
deliberately not expired on that rule — it is refreshed by the server own
polling, whose age `recentContact` already reports truthfully, and it has no
second source running ahead of it the way a lease does.

Arming is serialized against session setup. `POST /api/v1/settings` is refused
with 409 when it would arm automatic fan control or overtemperature standby
during a lease, and the decision is taken inside the same mutex that claims the
port, so an update cannot race a session being established. The reverse gate —
refusing a session while those controls are armed — is the same rule seen from
the other end, and the two read the armed set through the same definition.

A settings update is one transaction, and the boundary has to contain the read
as well as the commit. The request body is decoded first, outside every lock,
because decoding is network I/O of unbounded duration and a stalled client must
not be able to hold session setup; only then does the handler take its own
settings mutex, enter the passthrough boundary, and read the current settings,
merge, validate, decide arming and write. Reading the settings before the body —
as it once did — meant the arming decision compared a snapshot that a concurrent
disarm had already superseded, so both sides read armed, no transition was seen,
and the boundary was skipped entirely. The lock order is settings mutex, then
the passthrough setup mutex, then the config manager's own lock: session setup
already takes the setup mutex before reading the armed set from config, so
holding the config lock across the boundary would invert that pair and deadlock
against a connecting client.

Raw passthrough also requires polling. With `pollingMode: "off"` no serial
source is created, so there is nothing to lease; the settings route refuses that
combination with 400 rather than storing a configuration that asks for
passthrough and reports it unavailable. Validation lives in the route rather
than in config's own validators, which `LoadOrCreate` shares: refusing there
would turn a configuration already on disk into a startup failure, and a server
that will not start is a server whose overtemperature standby is not running.

`GET /api/v1/raw-passthrough` reports whether a client is connected,
`blockedByArmedControls` when a session would currently be refused, and
`tapFresh` for display freshness. `automaticControlsAvailable` reports serial
port ownership: false for the lifetime of a lease, true whenever no client holds
the port. It is not a statement that any control is armed —
`blockedByArmedControls` is what reports that. `enabled` means configured *and* running, so a
listener that failed to bind reports `enabled: false`; `listenerAvailable` and
the bind error in `note` are what separate that from a passthrough the operator
turned off. An operator is never left to infer safety that
is not present.

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
| `/api/v1/alarms` | GET | Canonical alarms route | Protocol-native warnings/alarms, configurable monitoring, and read-only reporting of the separately armed one-shot overtemperature standby controller |
| `/api/v1/fan-policy` | GET | Fan-policy state | Automatic hysteresis, manual override, persisted verification confidence/source, cooldown, ordered RX/TX pause-resume receipt, and explicit operator-recovery state |
| `/api/v1/fan-policy/override` | POST | Manual fan override | Requests automatic, Normal, or CONTEST through the display-verified experimental transaction; optional expiry defaults to until changed |
| `/api/v1/fan-policy/verify` | POST | Fan-mode verification | Queues a verified read/save transaction to refresh stale EEPROM-backed state |
| `/api/v1/fan-policy/recover` | POST | Failed fan recovery | Clears the latch and manual override only after automatic control is disabled and fresh STANDBY/RX home evidence is observed; sends no amp command |
| `/api/v1/menu-debug/session` | GET/POST | Menu discovery session | Inspects or arms a disabled-by-default, memory-only, revision-protected STANDBY/RX wizard; arming may clear only an inert, completed, display-verified Normal override without sending an amplifier command |
| `/api/v1/menu-debug/session/advance` | POST | Start guarded automatic test | One confirmation starts discovery plus frozen apply; every internal command retains its own authorization and newer matching display receipt |
| `/api/v1/menu-debug/session/verification` | POST | Operator verification | Records candidate/restoration results; positive candidate verification starts automatic restore, while failed or uncertain verification stops without recovery commands |
| `/api/v1/menu-debug/session/abort` | POST | Abort wizard | Releases ownership and sends no recovery or OPERATE command |
| `/api/v1/menu-debug/report` | GET | Sanitized diagnostic report | Returns completed evidence or an explicitly incomplete ordinary failure/abort/expiry report through the token-bearing UI; reconnect/model change invalidates access |
| `/api/v1/menu-debug/report.json` | GET | Download sanitized report | Downloads the current `menu-report.v2` JSON document |
| `/api/v1/menu-debug/report/upload` | POST | Consented report upload | Accepts only a complete report and explicit consent; incomplete diagnostics are never uploadable |
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
      │                                           │
      │                                           └──► checksum-valid LCD TX/RX + verified menu waypoints
      │
      └── status poll responses ──► protocol.StatusFromFrame ──► runtime.StatusState
                                                        │
                                                        ├──► /api/v1/status and /api/v1/status/ws
                                                        ├──► monitoring.Controller
                                                        └──► fanpolicy.Controller
```

Development/fallback mode:

```
fixtures/*.bin ──► protocol.LoadFixtureState ──► display.State/runtime snapshot
```

`display.State` remains the pivot for screen mirroring and rendered LCD output. `StatusState` is the pivot for machine-readable amp status: it prefers protocol-native status-poll data and fills only the remaining gaps from the current runtime/display snapshot.

---

## What is wired vs. what remains deliberately limited

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
| Temperature/SWR monitoring | Working; observational unless overtemperature standby is separately armed |
| Display-verified fan policy | Production support is hardware-confirmed on the First Series 1.3K-FA and CONFIG-first Second Series 1.5K-FA; unsupported models remain blocked before SET |

---

## Design constraints

- **Trusted-LAN station appliance.** This service is meant to run near the amp and be reached from operator machines on the same station LAN. It is not an internet-facing web app and should sit behind the user's LAN, VPN, firewall, or reverse proxy for any remote use.
- **Captured data beats theory.** If real frames disagree with assumptions, update the code and document the difference.
- **No vendor binaries.** The repo must not include vendor executables. Protocol/font provenance needs to be documented honestly before public release.
- **API must be boring and stable.** Prefer explicit JSON over cleverness. The canonical REST surface is the current `/api/v1/...` API; older non-v1 routes are compatibility holdovers, not the preferred contract.
- **Every feature needs tests.** Decoder changes need regression tests against fixture files.
- **Wake reserves actuation without holding the coordinator lock across serial I/O.** Manual writes, menu/fan leases, overtemperature acquisition, and a second wake fail busy while the reservation is active. Safety retries only on a newer authoritative status poll after wake completes, keeping the serial read callback nonblocking, and then preempts any lower-priority lease. Owner names are diagnostics only: every successful acquisition returns an opaque lease identity, and only that exact lease can release the current reservation.
- **Automatic standby is narrow and one-way.** It may send the documented OPERATE toggle once per latched overtemperature episode only with fresh protocol-native OPERATE and RX evidence. It never acts during TX, retries, powers off, or auto-wakes.
- **Fan navigation is one verified transaction.** Production writes require a promoted profile and support the model- and topology-bound First Series Expert 1.3K-FA and CONFIG-first Second Series Expert 1.5K-FA paths. Automatic, manual, and verification requests share the same temporary-STANDBY path, pause every write during TX, stop on unexpected screens, and restore OPERATE only after verified SAVE/home completion when the controller owned the transition.
- **Third Series 2K-FA production control is narrower than First/Second Series.** It is bound to `EXPERT 2K-FA`, the exact Third Series topology, and an explicit exact allowlist of operator-declared actual firmware values (`Rel.26_03_24_A` and `Rel.08_06_26_A`) because status polling does not expose firmware. Empty, mistyped, case-variant, or other firmware values fail closed. It maps hardware QUIET to logical Normal cooling and hardware NORMAL to logical high cooling, starts only from verified STANDBY/RX, and never sends DISPLAY or an OPERATE/STANDBY command. The original firmware's exact 23-state reversible D1 report is retained as regression evidence; the newer firmware has a separate read-only re-capture of the unchanged top setup grid and FAN NOISE page/cursor topology, not a claimed second reversible report. Menu Debug remains the guarded evidence path for other firmware or topology combinations.
