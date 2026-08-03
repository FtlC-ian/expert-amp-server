# Node-RED Integration Guide — Expert Amp Server

A project-shareable Node-RED example flow for Expert Amp Server.
This guide covers the example flow included at
`docs/integrations/node-red-example-flow.json`.

---

## Overview

The example flow is a **dashboard-fidelity port** of the WB2WGH SPE Linear flow (V1.3).
It preserves the original layout, CSS, button styles, gauge and level widgets, and
color-state logic, while rewiring all reads and writes from serial to the Expert Amp
Server `/api/v1` API. Live status is websocket-first via `/api/v1/status/ws`, with
HTTP polling retained as an automatic fallback path.

Thanks to **Ron, WB2WGH**, for publishing the original SPE Linear Node-RED flow that
this version is based on.
Reference announcement for the version used here:
<https://groups.io/g/nodered-hamradio/topic/expert_spe_linear_flow/116437717>

**Required Node-RED packages:**
- `node-red-dashboard`
- `node-red-contrib-ui-level`
- core Node-RED websocket nodes (built in; no extra install)

Install via the Node-RED palette manager or:

```bash
cd ~/.node-red
npm install node-red-dashboard node-red-contrib-ui-level
```

---

## What the flow does

### Live status + fallback

| Path | Mode | Interval / behavior | Endpoint |
|---|---|---|---|
| Primary live status | WebSocket | Connect once, receive initial snapshot + change-driven updates | `GET /api/v1/status/ws` |
| Fallback status | HTTP poll | Every 5 s only while websocket is down | `GET /api/v1/status` |
| Alarms | HTTP poll | Every 10 s | `GET /api/v1/alarms` |

### Dashboard groups (tab: "Expert Amp")

| Group | Width | Purpose |
|---|---|---|
| Display | 12 | Expert Amp display render image (`/api/v1/display/render.png`) |
| SPE | 6 | Main telemetry tiles + gauges + levels |
| SPE Auxiliary | 4 | Front-panel control buttons |

### SPE group tiles (order matches original WB2WGH flow exactly)

| Order | Tile | Type | Width | Notes |
|---|---|---|---|---|
| 1 | Model | status tile | 2 | Read-only; shows model name |
| 2 | ATU SWR | ui_level | 4 | Driven from `status.swr` |
| 3 | RX/TX | status tile | 2 | Green = Receive, Red/Yellow = Transmit |
| 4 | ANT SWR | ui_level | 4 | Driven from `status.antennaSwr`, falling back to `status.swr` |
| 5 | Mode | action tile | 2 | Green = Standby, Red = Operate; click sends `operate` |
| 6 | Output Power | ui_level | 4 | Driven from `status.powerWatts` (scaled 0–100%) when present |
| 7 | PWR Level | status tile | 2 | Color-coded by level + mode state |
| 8 | Power W gauge | ui_gauge | 2×2 | 0–1500 W |
| 9 | SWR gauge | ui_gauge | 2×2 | 1–3 |
| 10 | Antenna | action tile | 2 | Click cycles antenna; shows `status.antenna` |
| 11 | Input | action tile | 2 | Click cycles input; shows `status.input` |
| 12 | PA Volts gauge | ui_gauge | 2×2 | 0–60 V, driven from `status.paSupplyVoltage` |
| 13 | PA Amps gauge | ui_gauge | 2×2 | 0–60 A, driven from `status.paCurrent` |
| 14 | Bank | status tile | 2 | Read-only; shows `antennaBank` (bank select not in current API) |
| 15 | Tune | action tile | 2 | Static green; sends `tune` |
| 16 | Temp Lower / Comb gauge | ui_gauge | 2×2 | 0–120 °C, prefers lower/combiner status fields and falls back to upper temp |
| 17 | Logging State | badge | 2 | Shows "HTTP API" (file logging removed) |
| 18 | Band | status tile | 2 | Read-only; shows `status.band` |
| 19 | RX Antenna | status tile | 2 | Read-only; shows antenna + bank |
| 20 | Warnings | status tile | 3 | Blue = No Warnings, Orange = Warning present |
| 21 | Alarms | status tile | 3 | Blue = No Alarms, Red = Alarm active |
| 22 | Power On | orange button | 2 | Calls experimental `POST /api/v1/actions/wake` |
| 23 | USB Status | status tile | 1 | Green = websocket live, Orange = HTTP fallback, Red = API error |
| 24 | USB Sync | action tile | 1 | Triggers immediate HTTP refresh |
| 25 | Power Off | orange button | 2 | Sends `off` action |

### SPE Auxiliary group buttons (order matches original exactly)

| Order | Button | Action sent |
|---|---|---|
| 1 | Backlight Off | `backlight-off` |
| 2 | Backlight On | `backlight-on` |
| 3 | L - | `l-` |
| 4 | L + | `l+` |
| 5 | C - | `c-` |
| 6 | C + | `c+` |
| 7 | Band - | `band-` |
| 8 | Band + | `band+` |
| 9 | Set | `set` |
| 10 | Display | `display` |
| 11 | Left Arrow | `left` |
| 12 | Right Arrow | `right` |
| 13 | Fan Boost | Requests persistent CONTEST mode through `POST /api/v1/fan-policy/override` |
| 14 | Fan Normal | Requests persistent Normal mode through `POST /api/v1/fan-policy/override` |

---

## Configuration

Edit the **"Set expertAmpBaseUrl"** function node to point at your server:

```js
const baseUrl = 'http://amp-host.local:8088';  // your host
```

Default is `http://localhost:8088`.

Clean-import gotcha: the three `ui_level` nodes require their full `node-red-contrib-ui-level` configuration. If Node-RED logs `Cannot read properties of undefined (reading 'indexOf')` from `ui-level.js`, the flow was imported from a damaged or hand-edited export with incomplete level-node fields. Re-import the checked-in flow and verify `node-red-contrib-ui-level` is installed.

Also update the **websocket-client** config node if your server is not local:

```text
ws://amp-host.local:8088/api/v1/status/ws
```

Use `wss://...` if your deployment is TLS-terminated.

---

## Button actions

All buttons POST to `POST /api/v1/actions/button` with `{"name": "<action>"}`.

**Currently supported actions** (safe per transport/buttons.go):

```
set, left, right, up, down, display
input, antenna, band-, band+
l-, l+, c-, c+
tune, off, power, operate, cat
backlight-on, backlight-off
```

**Intentionally blocked** (not in current command table or not safely confirmed):

```
back, on, standby
```

Fan mode is not a raw front-panel action. The checked-in dashboard's **Fan Boost** and **Fan Normal** buttons POST `{"mode":"contest"}` or `{"mode":"normal"}` to `/api/v1/fan-policy/override`. Check `supportedModes` first: promoted First Series 1.3K-FA and Second Series 1.5K-FA profiles advertise `normal` and `contest`; unsupported models advertise no modes and fail closed before SET. These overrides remain active until explicitly changed unless an API client supplies `durationMinutes`; that countdown begins only after the requested mode is display-verified. Send `{"mode":"automatic"}` to return desired-policy selection to the temperature controller.

Production fan control supports the promoted First Series Expert 1.3K-FA and CONFIG-first Second Series Expert 1.5K-FA model-bound topologies after physical apply/restore verification on both models. Unsupported models are blocked before SET; F-KFA NORMAL/QUIET layouts remain topology-only.

The original WB2WGH macro is not safe to reproduce through generic button calls. It was fan-tested only on a 2K-FA, sends a fixed eleven-RIGHT menu sequence without reading the LCD, and can alter the wrong setting if the starting screen or one command differs. The server replaces it with the captured display-verified transaction.

Do not replace that macro with another blind sequence, including a 13-command Expert 1.5K-FA flow. Use the built-in Menu Debug wizard or its guarded API: one start runs discovery and apply automatically, but each command remains separately authorized and blocked until a newer matching LCD receipt arrives. The wizard then waits for physical candidate confirmation before automatic restore and waits again for confirmation that the original value returned. Incomplete diagnostics are visible and downloadable in the token-bearing built-in UI; they are never uploadable promotion evidence.

Use `GET /api/v1/fan-policy` for the disabled-by-default temperature/hysteresis decision and navigation receipt. Production control requires a promoted model profile and supports the verified First Series Expert 1.3K-FA and CONFIG-first Second Series Expert 1.5K-FA paths; unsupported models are blocked before SET. Active control requires polling mode `both` and starts only after fresh protocol and checksum-valid LCD RX evidence. The server verifies the model plus first SET-menu topology and then every waypoint. If initially OPERATE, it temporarily requests and verifies STANDBY because tested hardware ignores SET on the OPERATE home screen. After verified SAVE and return home it restores and verifies OPERATE only when this transaction originally disabled it.

If TX begins, the controller sends no writes, pauses its ordinary waypoint timeout, and resumes only after ordered protocol and LCD RX evidence reverifies the exact expected state. Fan-value toggles and SAVE are RX-only. The temporary STANDBY may skip an unattended transmit cycle. Overtemperature safety preempts fan control and suppresses OPERATE restoration. After stale or unknown status, unsupported state, timeout, ambiguous toggle, write failure, or screen mismatch, the controller never blindly restores OPERATE and requires the operator to verify STANDBY.

Failed navigation responses include the original `navigation.lastError` and `navigation.recoveryInstructions`; the checked-in flow prints both instead of replacing them with only the generic latch error. To recover, stop transmitting, put the amplifier in STANDBY, use the physical panel to return home, disable automatic fan-policy switching, and wait for fresh STANDBY/RX home evidence. Then use **Clear Failed Fan Transaction** in the built-in Settings page or `POST /api/v1/fan-policy/recover`. Recovery sends no amplifier command, clears the failed manual override, and does not restore OPERATE.

`POST /api/v1/fan-policy/verify` refreshes a receipt loaded as `persisted-stale` after server restart. While connected, a complete manually driven FAN MANAGEMENT → SAVE → STORING DATA → home sequence is also observed and recorded. A menu value seen before SAVE is never treated as the stored EEPROM mode.

---

## Preserved vs changed from original WB2WGH flow

### Preserved

- **SPE Master CSS** — all original classes intact:
  `SPEvibrate`, `SPEfilled`, `SPEtouched`, `SPErounded`, `SPEhoverblue`, `SPEhoverred`
- **Group layout** — SPE (width=6) and SPE Auxiliary (width=4) on one dashboard tab
- **All 25 SPE tile names and order** — identical to the original
- **All 14 SPE Auxiliary button names and order** — identical to the original
- **ui_gauge ×5** — same positions (orders 8, 9, 12, 13, 16) and 2×2 size
- **ui_level ×3** — ATU SWR, ANT SWR, Output Power at orders 2, 4, 6
- **Color-state logic** — RX/TX, Mode, PWR Level, Warnings, Alarms use same gradient rules
- **Tune static-green button** — preserved
- **Power On / Power Off orange buttons** with `SPEhoverred` hover style

### Changed

| Original | This flow | Reason |
|---|---|---|
| Serial port nodes | Websocket-first status via `/api/v1/status/ws` with HTTP fallback to `/api/v1/status` | No direct serial in Node-RED |
| USB status = physical USB | USB Status = websocket/API connectivity state | Serial path removed |
| USB Sync = serial resync | USB Sync = immediate HTTP refresh | Rewired to API transport |
| Logging State = CSV file | Logging State shows "HTTP API" | File logging not applicable |
| Gauge labels: Volts / Amps | Gauge labels now use promoted API fields for PA volts and PA amps | Driven from `/api/v1/status` instead of hidden note strings |
| Bank select buttons | Bank tile is read-only | Bank select not in current API |
| Fan Quiet / Fan Normal | Fan Boost / Fan Normal use the verified fan-policy override API | Raw fan buttons are not document-backed commands; the server performs the captured temporary-STANDBY transaction |
| Backlight On/Off | Document-backed `backlight-on` / `backlight-off` actions | Hardware effect remains model-dependent |
| Power On (Python script) | Calls experimental `POST /api/v1/actions/wake` | Experimental HTTP wake action |
| History charts / fidelity notes | Removed from the example | They added clutter and unnecessary dashboard weight for the current use case |

### Added (not in original)

- Display render image at top (Expert Amp Server feature, per project direction)

---

## Smoke-test checklist

Before declaring the flow healthy:

1. **Server reachable** — deploy, open Node-RED debug panel, confirm base URL message on startup
2. **Websocket/fallback status works** — confirm tiles update. USB Status should show green `WS Live` when websocket messages are flowing, or orange `HTTP Fallback` when the fallback poll path is active.
3. **Fallback recovery works** — interrupt websocket/server access; USB Status should turn red on API errors, then return to `WS Live` or `HTTP Fallback` after connectivity is restored.
4. **Tiles update** — open Node-RED Dashboard (`/ui`), confirm SPE tiles show values
5. **Display render loads** — Display group shows amp display image (may be blank in fixture mode)
6. **Safe button action works** — click Set or Display button; check debug tab for `OK: set` / `OK: display`
7. **Alarm tile updates** — `/api/v1/alarms` returns protocol-native warning/alarm lists plus safety-monitor state. Monitoring is observational unless the server's separate overtemperature standby arm flag is explicitly enabled; with no active vendor alarm, Warnings + Alarms should show blue `No Alarms / No Warnings`
8. **Error handling** — stop the Expert Amp Server completely; USB Status tile should turn red after fallback requests fail

---

## Required packages

| Package | Purpose |
|---|---|
| `node-red-dashboard` | Core dashboard nodes like `ui_template`, `ui_gauge`, `ui_chart`, `ui_group`, `ui_tab` |
| `node-red-contrib-ui-level` | Provides the `ui_level` nodes used for ATU SWR, ANT SWR, and Output Power bars |

If import complains about unrecognized `ui_level` node types, this second package is the missing dependency.

The websocket nodes used here are part of core Node-RED; no extra websocket package is required.

---

## Notes

- Websocket-first design for live status, with automatic HTTP fallback so the dashboard does not go stale on disconnect.
- Flow works against the canonical `/api/v1` surface. No direct serial wiring in Node-RED.
- `/api/v1/status/ws` carries the same canonical status model as `GET /api/v1/status` and is the preferred live feed.
- Stable automation fields include `outputLevel` as a string and `temperatureC` as a JSON number in canonical Celsius. For example, a Node-RED function can use `String(status.outputLevel || '')` for the power preset and `Number(status.temperatureC)` only after checking that the field is present. Do not parse `temperatureDisplay` for automation.
- `/api/v1/status` remains the fallback polling route and the authoritative machine-readable status surface for reconnect gaps and manual refreshes. It uses the vendor-documented status poll/response shape where available, with display-derived values only as fallback. Protocol-backed fields now include raw-plus-decoded band and warning/alarm fields (`bandCode`/`bandText`, `warningCode`/`warningsText`, `alarmCode`/`alarmsText`), the raw `atuStatusCode`, and first-class meter fields such as `antennaSwr`, `paSupplyVoltage`, `paCurrent`, `temperatureLowerC`, and `temperatureCombinerC` when the amp model reports them. All `*C` fields are canonical Celsius; set `amplifierTemperatureUnit` to match the amplifier SET menu because the status response itself omits the unit.
- The checked-in example intentionally matches the cleaned-up working dashboard: no History group, no extra chart panels, and a tighter display block.
- All node IDs are stable across re-imports (MD5 of seed name). Existing nodes will update on re-import.
- The flow is project-clean: no Ian-specific hostnames, entity IDs, or HA-specific details.
