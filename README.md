# expert-amp-server

Early Go implementation for the SPE reverse-engineering and local control work that is becoming Expert Amp Server.

## Current scope

This repo starts with the renderer and local API surface because those are the parts we understand with the most confidence today:

- 8 row x 40 column display model
- built-in SPE-style 8x8 LCD font tables used by the renderer
- project-defined custom glyph overrides for SPE-style LCD symbols
- per-cell attribute bytes controlling invert or highlight and alternate glyph bank selection
- one shared in-memory runtime snapshot for display, telemetry, and frame metadata


## Network and safety model

Expert Amp Server is built as a trusted-LAN station appliance. The default listen address `:8088` intentionally accepts connections from other machines on the local network, because the normal operating model is a radio-side Pi serving a browser, logger, Node-RED, or radio-control PC elsewhere on the same LAN.

Do not expose this service directly to the public internet. The HTTP API includes state-changing routes for amp controls, settings, wake, and restart, and it does not currently implement authentication. Put it behind a trusted station network, VPN, firewall, or reverse proxy if you need remote access.

## Preview

```bash
cd expert-amp-server
go run ./cmd/render-preview -out preview.png
```

That writes a quick PNG render using the built-in LCD font tables and custom SPE-style glyphs.

## Tiny local server

```bash
cd expert-amp-server
go run ./cmd/server -addr :8088 -poll-interval 200ms
```

On first run the server writes a local JSON config file at `config/expert-amp-server.json` unless you override it with `-config`. That default relative path is mainly a local dev convenience. For production or service installs, pass an explicit `-config /path/to/expert-amp-server.json` so the runtime file lives somewhere intentional.

Example default config:

```json
{
  "serialPort": "",
  "listenAddress": ":8088",
  "pollIntervalMs": 200,
  "displayPollingEnabled": true,
  "statusPollingEnabled": true
}
```

If `serialPort` is empty, the watch UI shows a first-run banner linking to the Settings tab, and the API reports `needsSetup: true` from `GET /api/v1/settings`.

The server runtime path is intentionally simple:

- one background poller or ingest loop
- one in-memory snapshot store
- request handlers read that snapshot by default
- fixture reads stay available for protocol and renderer inspection

Build/version metadata defaults to `dev` for local runs. Release builds can inject values with Go linker flags, for example:

```bash
go build -ldflags "-X main.Version=$(git describe --tags --always) -X main.Commit=$(git rev-parse --short HEAD) -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.Channel=stable" ./cmd/server
```

The server logs those values at startup and exposes them at `GET /api/v1/version`. `/healthz` remains a plain process liveness check and includes `X-Expert-Amp-Version` for simple supervisors.

## Canonical API, phase 1

The stable phase 1 surface is under `/api/v1/...`.

### Display

- `GET /api/v1/display/state`
  - returns `{ success, data }`
  - `data.state` is the current 8x40 runtime snapshot by default
  - optional `?source=protocol` or `?kind=home|menu|panel` returns decoded fixture state without mutating runtime state
- `GET /api/v1/display/frame`
  - returns runtime frame metadata by default
  - optional `?kind=home|menu|panel` returns fixture frame metadata
- `GET /api/v1/display/render.png`
  - renders the current runtime display as PNG
  - supports the same optional fixture override query params as the state route
- `GET /api/v1/display/render.svg`
  - renders the current runtime display as SVG
  - supports the same optional fixture override query params as the state route
- `GET /api/v1/display/ws`
  - websocket feed that emits a lightweight event whenever the authoritative runtime display snapshot changes
  - intended for the built-in watch page and other image-based clients that want to refresh `/api/v1/display/render.png` only when a new frame is available
  - legacy HTTP render endpoints remain available for polling consumers

Example:

```bash
curl http://localhost:8088/api/v1/display/state | jq
curl "http://localhost:8088/api/v1/display/frame?kind=home" | jq
curl -o screen.png http://localhost:8088/api/v1/display/render.png
```

### Telemetry and alarms

- `GET /api/v1/status`
  - preferred automation polling surface for amp status
  - authoritative source order is: protocol-native vendor status poll data first, current runtime telemetry snapshot second
  - keeps protocol-native status truth separate from display/render/runtime snapshot truth, while giving both REST and websocket clients the same selection behavior
  - includes `recentContact` and `lastContactAt` so clients can light a conservative "we are hearing from the amp recently" indicator without confusing that with operate or TX state
  - already uses status-payload identifier/state bytes for fields like `modelName` and `operatingState` when available
  - exposes protocol-backed raw fields like `bandCode`, `warningCode`, `alarmCode`, and `atuStatusCode`, plus decoded companions `bandText`, `warningsText`, and `alarmsText` (with `warnings` and `activeAlarms` retained as compatibility aliases of the decoded lists)
  - promotes documented status-poll meter fields into first-class API fields, including `antennaSwr`/`antennaSwrDisplay`, `paSupplyVoltage`/`paSupplyVoltageDisplay`, `paCurrent`/`paCurrentDisplay`, and model-conditional lower/combiner temperature fields
  - keeps display-derived fallback for fields the current status poll surface still does not provide directly
- `GET /api/v1/status/ws`
  - websocket feed for the same canonical status model returned by `GET /api/v1/status`
  - sends one status snapshot immediately after connect, then pushes changed status snapshots only
  - fully event-driven from the authoritative status path: one clock, no secondary websocket polling cadence
  - `pace` query strings are not supported on this canonical endpoint
- `GET /api/v1/telemetry`
  - returns the current runtime telemetry snapshot inside the standard envelope
  - this is still useful for UI and fallback values, but it is not the authoritative machine-readable status contract when `/api/v1/status` has protocol-native data
  - currently exposes the runtime telemetry snapshot used by the UI and fallback paths, including `modelName`, `operatingState`, `mode`, `tx`, `band`, `input`, `antenna`, `antennaBank`, `catInterface`, `outputLevel`, `swr`, `swrDisplay`, `temperatureC`, `temperatureDisplay`, plus source/confidence metadata
  - fields that are still unknown, including `frequency` and `powerWatts` for current captured home frames, stay omitted and are called out in `notes` instead of guessed
- `GET /api/v1/alarms`
  - returns an honest phase 1 stub inside the standard envelope
  - today this is `{ active: [], stub: true }` plus source metadata

Example:

```bash
curl http://localhost:8088/api/v1/status | jq
curl http://localhost:8088/api/v1/telemetry | jq
curl http://localhost:8088/api/v1/alarms | jq
# websocket example
# websocat 'ws://localhost:8088/api/v1/status/ws'
```

### Runtime and config

- `GET /api/v1/runtime`
  - returns effective polling settings and last snapshot update time
- `GET /api/v1/runtime/snapshot`
  - returns the full runtime snapshot inside the standard envelope
- `GET /api/v1/runtime/ingest`
  - returns current serial ingest diagnostics inside the standard envelope
- `POST /api/v1/runtime/restart`
  - requests a clean process exit so a service supervisor can bring the server back with saved settings
  - this is an explicit admin action from the Settings page, not an auto-restart on save
  - if the server was launched manually without systemd, launchd, Docker restart policy, or similar supervision, it will stop and stay stopped until you start it again
- `GET /api/v1/settings`
  - gets the local runtime config
- `POST /api/v1/settings`
  - saves local runtime config; accepts the advanced serial parameters that are actually user-configurable
  - serial port, listen address, and display-frame refresh interval take effect after restart
  - `displayPollingEnabled` and `statusPollingEnabled` are live toggles and take effect immediately
  - `pollIntervalMs` is the backend display-frame refresh cadence, not a generic page refresh rate
  - `statusPollIntervalMs` is the backend protocol-status refresh cadence used to keep `/api/v1/status` and `/api/v1/status/ws` current in memory
  - full accepted field set: `serialPort`, `listenAddress`, `pollIntervalMs`, `displayPollingEnabled`, `statusPollingEnabled`, `serialBaudRate`, `serialReadTimeoutMs`, `statusPollCommandEnabled`, `statusPollIntervalMs`, `serialAssertDTR`, `serialAssertRTS` (legacy `serialPollEnabled` and `serialPollIntervalMs` are still accepted as backward-compatible aliases; the actual documented status poll frame is fixed in code rather than user-editable)
- `GET /api/v1/serial-ports`
  - enumerates available serial ports with USB metadata for discovery
- `GET /api/v1/openapi.json`
  - serves the current machine-readable OpenAPI document for the canonical phase 1 API surface
- `GET /api/v1/docs`
  - serves a local human-facing API docs page backed by the same OpenAPI artifact

Example:

```bash
curl http://localhost:8088/api/v1/runtime | jq
curl http://localhost:8088/api/v1/runtime/snapshot | jq
curl http://localhost:8088/api/v1/settings | jq
curl http://localhost:8088/api/v1/serial-ports | jq
curl http://localhost:8088/api/v1/version | jq
curl http://localhost:8088/api/v1/openapi.json | jq '.info, .paths | keys?'
open http://localhost:8088/api/v1/docs
curl -X POST http://localhost:8088/api/v1/settings \
  -H 'Content-Type: application/json' \
  -d '{
    "serialPort": "/dev/ttyUSB0",
    "listenAddress": ":8088",
    "pollIntervalMs": 200,
    "displayPollingEnabled": true,
    "statusPollingEnabled": true,
    "serialBaudRate": 115200,
    "serialReadTimeoutMs": 250,
    "statusPollCommandEnabled": true,
    "statusPollIntervalMs": 125,
    "serialAssertDTR": true,
    "serialAssertRTS": true
  }'
```

### Actions

- `POST /api/v1/actions/button`
  - sends a real document-backed front-panel transport frame over the local serial path
  - currently wired action set from the newer Programmer's Guide command table: `input`, `band-`, `band+`, `antenna`, `l-`, `l+`, `c-`, `c+`, `tune`, `off`, `power`, `display`, `operate`, `cat`, `left`, `right`, `up`, `down`, `set`
  - `up` and `down` remain aliases for the documented combined front-panel `[◄▲]` and `[▼►]` keys, so they intentionally share the same transport codes as `left` and `right` until hardware testing confirms exact per-mode effects
  - the newly enabled direct commands come from the documented one-byte command table in the newer SPE Application Programmer's Guide variant already referenced by the repo protocol notes, but most still need physical confirmation on a real amp before we claim semantics beyond the docs
  - when live serial ingest is active, writes go through the already-open held port so button presses do not fight the background reader for the device
  - when live ingest is not active but a serial port is configured, the endpoint opens the configured serial device long enough to write the button frame
  - if no serial transport is available, the endpoint returns an honest unavailable error
  - still intentionally rejected for now: `back`, `on`, `standby`
  - failures return visible JSON errors with transport details for debugging
  - caution: exposing a command here means the transport code is document-backed, not that every user-visible effect is fully hardware-confirmed yet

- `POST /api/v1/actions/wake`
  - experimental power-on / wake path that toggles DTR and RTS on the FTDI serial control lines
  - this is intentionally separate from `POST /api/v1/actions/button`: it is not a normal front-panel button command and should be treated as a wake-specific transport path
  - when live serial ingest is active, the server closes the held serial handle, briefly opens the configured device for the wake sequence, then lets the normal reader reconnect
  - when live ingest is not active but a serial port is configured, the server opens the configured serial device just long enough to perform the wake sequence
  - canonical action name for this path is `wake`; do not send `on` to the button endpoint expecting the same behavior
  - hardware validation is still pending across amps/cabling; this route exists so testers can exercise the best-known Go implementation without treating it as proven

Example:

```bash
curl -X POST http://localhost:8088/api/v1/actions/wake | jq
```

Example:

```bash
curl -X POST http://localhost:8088/api/v1/actions/button \
  -H 'Content-Type: application/json' \
  -d '{"name":"set"}' | jq

curl -X POST http://localhost:8088/api/v1/actions/button \
  -H 'Content-Type: application/json' \
  -d '{"name":"band+"}' | jq

curl -X POST http://localhost:8088/api/v1/actions/button \
  -H 'Content-Type: application/json' \
  -d '{"name":"back"}' | jq
```

## Response envelope and errors

Canonical JSON endpoints use this envelope where practical:

```json
{
  "success": true,
  "message": "optional human-readable message",
  "data": {},
  "error": ""
}
```

Errors from the `/api/v1/...` JSON routes return `success: false` with an `error` string.

## Watch UI

Visit `http://<host>:8088/` to see the local watch and control page.

The UI has two tabs: **Watch** and **Settings**.

### Watch tab

The Watch tab mirrors the physical front-panel layout shared across the SPE Expert line:

- **Display mirror** — live-refreshed render of the current amp display (white-on-black, refreshed websocket-first when the backend runtime snapshot advances, with snapshot polling fallback if websocket delivery is unavailable)
- **Live status indicators** — the front-panel lamps now subscribe to the canonical `/api/v1/status/ws` feed for honest live TX, operate, alarm, and recent-contact updates, with REST polling fallback if websocket delivery is unavailable
- **Source badge** — shows whether the current snapshot is `live` (real serial), `fixture`, or unknown
- **Front panel controls** — the panel layout wires the documented button positions that have direct codes in the newer Programmer's Guide table:
  - **INPUT**, **ANT**, **TUNE**, **DISP**, **OP**, and **CAT**
  - **◄BND / BND►**, **◄L / L►**, **◄C / C►**
  - **◄▲** (left / up, code `0x0f`), **▼►** (right / down, code `0x10`), **SET** (`0x11`), **DISPLAY** (`0x0c`)
- **Honesty note** — wired controls are document-backed and transport-real, but still need physical button-by-button confirmation on real hardware before every label/effect should be treated as fully trusted
- **Blocked actions** — **Back** and **Standby** remain unavailable as normal front-panel button actions; **On** uses the separate experimental wake endpoint because it is a control-line behavior, not a normal button command
- **API quick-reference** — all canonical v1 endpoints in a collapsible section in the Settings tab

The display area always uses white-on-black rendering regardless of browser theme, matching the real amp display.

### Settings tab

The Settings tab provides one clear place to configure the server without touching the config file directly.

**Display Preferences** (browser-local, labeled honestly):
- Watch layout: Front Panel or Operator
- Display scale: 1× through 6×
- LCD polarity: Inverted (white-on-black, default) or LCD Native (black-on-white) via CSS filter, does not affect the server or API

**Connection** (saved to server config; serial port, listen address, and snapshot poll interval take effect after restart; display/status toggles are live):
- Serial port — with a live port picker that enumerates ports the server can see
- Listen address
- Poll interval (ms)
- Display polling enabled / Status polling enabled (both are live toggles)
- Restart Server button — explicitly requests a clean process exit so a supervisor can restart the service with saved settings

**Advanced Serial** (collapsed by default; saved to server config; take effect after restart):
- Baud rate
- Read timeout (ms)
- Protocol status refresh enabled / Status command refresh interval (ms)
- Assert DTR / Assert RTS

Restart behavior is intentionally small and boring: the app asks the current expert-amp-server process to shut down cleanly, then relies on the same host-side service wrapper already managing it to start it again. The app does not try to install or manage system services itself.

## Compatibility routes

The preferred REST surface is the canonical `/api/v1/...` API documented above. The older routes below still work as compatibility aliases or legacy holdovers so existing experiments do not break immediately, but new clients should target the matching `/api/v1/...` route instead.

| Legacy route | Method | Prefer instead |
| --- | --- | --- |
| `/state` | GET | `/api/v1/display/state` |
| `/render.png` | GET | `/api/v1/display/render.png` |
| `/render.svg` | GET | `/api/v1/display/render.svg` |
| `/api/telemetry` | GET | `/api/v1/telemetry` |
| `/api/status` | GET | `/api/v1/status` |
| `/api/frame` | GET | `/api/v1/display/frame` |
| `/api/runtime` | GET | `/api/v1/runtime` |
| `/api/runtime/snapshot` | GET | `/api/v1/runtime/snapshot` |
| `/api/runtime/ingest` | GET | `/api/v1/runtime/ingest` |
| `/api/actions/button` | POST | `/api/v1/actions/button` |
| `/diff` | GET | no canonical v1 alias; this remains a legacy debug/demo helper |

The watch UI points at the canonical `/api/v1/...` endpoints. For display refresh specifically, it now prefers `/api/v1/display/ws` to know when `render.png` should be reloaded, instead of blindly reloading the image on a fixed browser timer.

## OpenAPI and local docs

The app now serves its own API documentation locally:

- `GET /api/v1/openapi.json` is the machine-readable OpenAPI artifact for the canonical phase 1 surface only
- `GET /api/v1/docs` is a small local docs UI that reads the served OpenAPI document and lists the current canonical endpoints, parameters, and envelope shapes

This documentation is intentionally conservative. It calls out current phase 1 realities instead of pretending unfinished behavior is stable, especially around status semantics, alarm stubbing, compatibility aliases, and settings field names.


## Documentation map

Detailed docs live under [`docs/`](docs/README.md): install notes, architecture, protocol notes, public-testing plan, and integration guides.

## Raspberry Pi install notes

For the current repeatable Pi/radio-host deployment shape, see [`docs/INSTALL_PI.md`](docs/INSTALL_PI.md). That document covers the known-good binary copy path, explicit config file, serial permissions, the starter systemd unit, sample config, release-build helper, sanity checks, and wake/button caveats.

## Safety and warranty

This project is experimental local-control software for expensive RF hardware. The implemented transport paths are believed to match documented or observed behavior, but they are not proven safe across all hardware, firmware, cabling, or station configurations. There is no warranty, no guarantee of fitness, and no promise that a command cannot behave differently on another amp, firmware revision, wiring setup, or station. The operator is responsible for deciding whether to run it and accepts the risk of using it.

Bug reports, hardware observations, and proposed changes are welcome, but this project does not provide commercial support or assume liability for station, amplifier, antenna, or operating damage.

## Node-RED integration

A dashboard-fidelity Node-RED example flow and guide are included:

- `docs/integrations/node-red.md`
- `docs/integrations/node-red-example-flow.json`

The flow is a faithful port of the WB2WGH SPE Linear flow (V1.3) to the Expert Amp Server
`/api/v1` HTTP API. It preserves the original CSS, button styles, group layout
(SPE width=6, SPE Auxiliary width=4), all 25 SPE tile names and order, all 14 Auxiliary
button names and order, the five ui_gauge widgets, the three ui_level bars, and the
original color-state logic for RX/TX, Mode, PWR Level, Warnings, and Alarms.

The example intentionally covers:

- websocket-first live status via `GET /api/v1/status/ws`, with HTTP fallback polling for `GET /api/v1/status` (5 s) and `GET /api/v1/alarms` (10 s)
- Expert Amp display render at the top from `/api/v1/display/render.png`, invalidated via `/api/v1/display/ws`
- dashboard-style control surface with color-changing status tiles, gauges, and levels
- all front-panel button actions through `POST /api/v1/actions/button`
- error catch node that marks USB Status red on HTTP failure

The shipped flow does not expose blocked actions (`back`, `on`, `standby`) or
unconfirmed actions (`fan-quiet`, `fan-normal`).

The shipped Node-RED example now uses `/api/v1/display/ws` to invalidate the display render image on real snapshot changes, matching the built-in watch UI's websocket-first refresh model. The plain HTTP render endpoints remain available for integrations that prefer polling.

Important honesty note: the vendor Programmer's Guide defines the status poll command as `555555019090`, and this worktree now parses the documented status response shape for `/api/v1/status`. The live runtime now drives display refresh and protocol-native status polling independently, so restoring the status poll no longer starves the display path. The status poll remains the authoritative automation source. The response stays intentionally conservative: protocol-backed fields are exposed where the guide and implementation justify them, including raw-plus-decoded band and warning/alarm fields, while display-derived fallback is still used only for fields the current status poll surface does not provide directly.

**Required packages:** `node-red-dashboard` and `node-red-contrib-ui-level`

## Next steps

- keep the runtime snapshot path boring and stable while live transport improves behind it
- validate the newly exposed document-backed commands on real hardware, especially the broader direct table (`input`, `band-`, `band+`, `antenna`, `l-`, `l+`, `c-`, `c+`, `tune`, `off`, `power`, `operate`, `cat`) plus the existing `up`, `down`, and `display` mappings, before claiming their effects are fully confirmed
- replace the alarm stub with decoded alarm state when that exists honestly in the snapshot
- keep the served OpenAPI artifact current with the phase 1 surface
- consider a shared in-memory broadcaster if later websocket work needs lower-latency fanout than the current simple per-connection change loop
- validate alt-bank attribute semantics against more real captures

## License

MIT. See [`LICENSE`](LICENSE).
