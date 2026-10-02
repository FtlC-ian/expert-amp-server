# Expert Amp Server

Expert Amp Server is a small Go server for local-network monitoring and control of SPE Expert amplifiers. Run it near the amp, open it from a browser, and get a practical amp panel without needing the vendor Windows app on the operating computer.

It is meant for radio-side computers like a Raspberry Pi, an Apache Labs ANAN G2 internal Raspberry Pi Compute Module, or any small Linux box that can talk to the amp's built-in USB Type-B control port.

## What it does

- Mirrors the SPE LCD display in a browser.
- Provides Front Panel and compact Operator layouts for day-to-day amp monitoring and control.
- Gives you local-network amp controls: operate/standby, power level, antenna, input, tune, display/menu controls, and power-off where supported.
- Adds display-verified manual and automatic fan cooling with timed Fan Boost, plus optional state-gated overtemperature standby protection.
- Includes a disabled-by-default Menu Debug & Reporting wizard for automatic evidence-gated fan compatibility tests and sanitized reports; bank topology remains available to explicit API clients.
- Exposes a documented HTTP/WebSocket API for custom integrations, station dashboards, Node-RED flows, and future Thetis-style gauge work.
- Ships a bootstrappable Node-RED dashboard example.
- Includes Raspberry Pi/systemd install notes and a release build helper.

## What you need

- An SPE Expert amplifier. Development and fan-control hardware testing use a First Series Expert 1.3K-FA. Users have also reported the server working on Expert 1.5K-FA and 2K-FA amplifiers; one 2K-FA installation uses the confirmed 57600 troubleshooting fallback.
- A Raspberry Pi or other small Linux computer near the amp.
- A USB cable from that computer to the amp's built-in USB Type-B control port. Do not use the separate CAT radio serial ports for Expert Amp Server.
- A trusted station LAN so your browser, Node-RED host, logger, or radio-control computer can reach the server.

One practical setup is an Apache Labs ANAN G2 / G2 Ultra with its internal Raspberry Pi Compute Module connected directly to the amp over USB. If you use Saturn/p2app remotely, see also the related p2app SPE CAT work for feeding band/frequency/TX state to the amp from the radio side.

## Screenshots

### Front Panel

![Front Panel layout](docs/reference/screens/watch-panel/front-panel-default.png)

### Operator

![Operator layout](docs/reference/screens/watch-operator/operator-default.png)

### Compact Operator

![Compact Operator layout with LCD hidden](docs/reference/screens/watch-operator/operator-narrow-lcd-hidden.png)

### Node-RED dashboard example

![Node-RED dashboard example](docs/reference/screens/node-red/node-red-dashboard-650-display-open-operate.png)

## Network and safety model

Expert Amp Server is a trusted-LAN station appliance. The default listen address `:8088` intentionally accepts connections from other machines on the local network because the normal setup is a radio-side Pi serving a browser, logger, Node-RED instance, or radio-control PC elsewhere on the same LAN.

Do not expose it directly to the public internet. The HTTP API includes state-changing routes for amp controls, settings, wake, and restart, and it does not currently implement authentication. Use a trusted station LAN, VPN, firewall, or reverse proxy for remote access.

This software controls RF hardware. It is believed to match the documented and observed transport behavior, but you are responsible for deciding whether it is appropriate for your station and amplifier.

## Quick start for local development

```bash
git clone https://github.com/FtlC-ian/expert-amp-server.git
cd expert-amp-server
go run ./cmd/server -addr :8088 -poll-interval 125ms
```

Open <http://localhost:8088/>.

If no serial port is configured, the app starts in setup/fixture mode and the Settings tab lets you enter a persistent config. For real installs, prefer an explicit config path; see the Raspberry Pi install guide.

The advanced `serialAssertDTR` and `serialAssertRTS` settings default to `true` when absent. Set them to `false` for transports such as PTYs or network serial bridges that do not support modem-control ioctls; explicit `false` values remain preserved across settings saves and restarts.

Third Series Expert 2K-FA production fan control additionally requires the operator-declared actual `fanPolicyFirmwareVersion` setting to equal `Rel.26_03_24_A` or `Rel.08_06_26_A` exactly. Status polling does not attest firmware, so empty, mistyped, case-variant, or other values advertise no supported modes and remain blocked before any amplifier command.

### Raw serial-over-TCP passthrough (optional, off by default)

`rawPassthroughEnabled` exposes the amplifier's serial link on
`rawPassthroughListenAddress` (default `:7388`, matching the SPE-LAN-UNIT's own
virtual COM port) so a single external client — such as SPE Expert Controller
over TCP/IP — can drive the amplifier through the machine already running this
server, with no second cable.

**It requires a configured `serialPort` and a `pollingMode` other than `off`.**
Passthrough leases the serial source, and no serial source is created while
polling is off, so the two settings contradict each other: `POST
/api/v1/settings` refuses the combination with HTTP 400 rather than saving a
configuration that asks for passthrough and reports it unavailable. A
configuration already on disk still starts — a conflict you can fix with one
request should not stop the server and take overtemperature standby down with
it — and `GET /api/v1/raw-passthrough` says in `note` which setting won.

The amplifier tolerates one serial master, so this is an exclusive lease. While
a client is connected the server stops its own polling and refuses its own
button and wake writes with HTTP 409; on disconnect it reclaims the port and
resumes automatically. A second concurrent client is rejected.

**Server-side automatic controls are unavailable during a session, and a session
is refused rather than started while one is armed.** If automatic fan control or
overtemperature standby is armed, a connecting client is refused: the server
closes the connection without sending a byte, so the client sees a clean EOF (or
`ECONNRESET` if it wrote first) and some clients show only a retry spinner. The
reason is not delivered on the raw socket — that socket carries the amplifier's
stream and nothing else. Read it from the server log or
`GET /api/v1/raw-passthrough`, which names each control to disarm. Nothing is
silently suspended and nothing is automatically restored afterwards — choosing
passthrough means knowingly running without those controls for as long as the
client is connected. The amplifier's own firmware stepdown is unaffected.

The dashboard and API keep working meanwhile: the server passively decodes the
amplifier→client direction as it forwards it, so while the external client polls
status, telemetry keeps updating. That state is labelled
`provenance: "passthrough-tap"`, is shown for display only, goes stale normally
when the client stops polling, and is never accepted as authority to actuate the
amplifier. The server injects no bytes of its own while the lease is held.

A client that never polls `0x90` is normal — Expert Controller Plus reads the
display and nothing else, in receive and under transmit alike. In that case
there is no tapped status to show, so canonical status reports display-derived
state alone for the session: the pre-lease status-poll reading is dropped as the
lease begins rather than served as though something were still refreshing it.

What that looks like in practice depends on the field. Values the amplifier also
prints on its LCD — temperature, output level, SWR, TX — keep being reported,
because the display tap is still decoding them, but they arrive as
`provenance: "display-frame"` with display-derived confidence rather than as a
status poll. Values only the status reply carries, such as the protocol band
code and text, drop out until polling resumes. `recentContact` follows the
display snapshot, which advances when the decoded screen changes rather than on
every frame, so a static screen ages out of the contact window while frames are
still arriving.

A client that does ask for `0x90` gets its reply reported as
`provenance: "passthrough-tap"`, but only while it keeps asking. Nothing obliges
it to, so a tap that stops being refreshed expires back to display-derived state
rather than outranking newer display telemetry indefinitely — the same place a
lease that never tapped anything starts from.

Check `GET /api/v1/raw-passthrough`: it reports `blockedByArmedControls` when a
session would be refused, `tapFresh` for display freshness, and
`automaticControlsAvailable`, which reports whether the server owns the serial
port — false for the lifetime of a lease, true when no client is connected. It
does not report whether a control is armed; `blockedByArmedControls` does that.
`listenerAvailable` reports whether the TCP listener actually bound. `enabled`
means configured *and* running, so a listener that failed to bind reports
`enabled: false` alongside `listenerAvailable: false`, with the bind error in
`note` — that pair is what tells a passthrough that could not start from one you
turned off.

Arming automatic fan control or overtemperature standby is refused with HTTP 409
while a client holds the port. Those controls cannot act during a lease, so
accepting the change would leave a control that reads as armed and does nothing.
Disarming is always allowed. `rawPassthroughEnabled` and
`rawPassthroughListenAddress` take effect on restart, because the listener is
built from the startup configuration.

## API highlights

Canonical API routes live under `/api/v1/...`.

Useful starting points:

```bash
curl http://localhost:8088/healthz
curl http://localhost:8088/api/v1/version
curl http://localhost:8088/api/v1/status
curl http://localhost:8088/api/v1/runtime/snapshot
curl http://localhost:8088/api/v1/display/text | jq
curl -o screen.png http://localhost:8088/api/v1/display/render.png
```

`GET /api/v1/display/text` provides the current eight-row LCD as fixed-width decoded text, including highlighted ranges and selected text for screen-reader clients. The bundled `display-capture` command can passively record unique display states using GET requests only. See [Display accessibility and passive capture](docs/DISPLAY_ACCESSIBILITY.md) for the response contract and capture workflow.

The app also serves:

- OpenAPI JSON at `/api/v1/openapi.json`
- local API docs at `/api/v1/docs`
- status websocket at `/api/v1/status/ws`
- display invalidation websocket at `/api/v1/display/ws`

## Fan cooling and temperature safety

Manual Fan Boost, Fan Normal, Fan Auto, and Verify controls use a display-verified transaction instead of a blind button macro. The controller temporarily enters STANDBY when necessary, pauses all menu writes during TX, verifies each expected LCD screen, saves the setting, and restores OPERATE only when it owned that transition. A timed Fan Boost begins its timer only after CONTEST mode is verified.

Production fan control is hardware-confirmed on the First Series Expert 1.3K-FA, CONFIG-first Second Series Expert 1.5K-FA, and the exact firmware-bound Third Series Expert 2K-FA topology. The API advertises `supportedModes` per promoted model; these profiles report `normal` and `contest`. For Third Series, hardware QUIET maps to logical Normal and hardware NORMAL maps to logical high cooling (`contest`). That profile starts only from verified STANDBY/RX and never sends DISPLAY or an OPERATE/STANDBY command. Unsupported model or firmware combinations report no modes and stop before SET; an unexpected screen stops the transaction rather than continuing.

Each navigation receipt retains the newest 32 exact verified semantic LCD waypoints in `navigation.waypointTrace`, suppresses consecutive duplicates, and reports any one-shot DISPLAY recovery attempt and recognized origin separately. It never includes raw LCD content, and a replacement transaction starts with a fresh trace.

Unattended startup verification has one narrowly reviewed recovery capability for the First Series 1.3K-FA and Second Series 1.5K-FA: after a mismatch or timeout while actively traversing that profile's exact setup-grid waypoint, it may send DISPLAY once and retain ownership until a newer checksum-valid STANDBY/RX home frame arrives. It never runs from a submenu, stale or cross-family screen, TX or unknown RX state, another model, or a manual/API transaction. Recovery does not make verification successful; the failed transaction remains latched until explicitly cleared.

Automatic fan cooling is disabled by default. Optional overtemperature standby is separately armed, requires fresh protocol-native OPERATE and RX status, sends one documented OPERATE toggle per hot episode, and never retries, powers off, or wakes the amplifier.

Important upgrade note: the amplifier status protocol sends temperature numbers without a unit. Before enabling temperature monitoring or fan automation, set `amplifierTemperatureUnit` to match the amplifier SET menu (`C` or `F`). All thresholds and API fields ending in `C` remain canonical Celsius.

SPE documents a maximum rate of `115200` with automatic adaptation to lower speeds, so that remains the server default. One field-tested 2K-FA installation only communicated reliably at `57600`; use that as a troubleshooting fallback, not as a model-wide requirement.

## Menu Debug & Reporting

The advanced Menu Debug wizard is disabled by default and intended for supervised compatibility testing. Arming requires the exact acknowledgement, fresh protocol STANDBY/RX, a checksum-valid STANDBY home display, inactive fan control, disarmed overtemperature standby, and an exclusive actuation lease. The built-in wizard requests only the fan capability, so successful fan verification finalizes its report without silently continuing into separate bank discovery; API clients may still request bank topology explicitly. One guarded start runs discovery and the frozen apply plan automatically, but every command still receives one authorization and waits for newer matching display evidence before the next command. The wizard pauses for physical candidate confirmation, restores automatically only after a positive answer, then pauses again for physical restored confirmation. A serial reconnect or an observed change to a different identified model invalidates the entire active session and discards all report evidence, so different hardware cannot continue or relabel it.

The reviewed First Series Expert 1.3K-FA profile may propose reversible fan and two-bank A/B tests. The Expert 1.5K-FA Second Series fan profile follows its captured CONFIG-first topology; both fan profiles have physically verified apply and restoration evidence and are eligible for production fan control. Unknown models and F-KFA NORMAL/QUIET layouts remain topology-only; QUIET is never treated as high cooling. TX is latched immediately. TX, stale evidence, an unexpected screen, timeout, abort, transport failure, or safety preemption stops the session fail-closed; the wizard never retries blindly, restores OPERATE, or performs blind menu recovery.

The reporting wizard retains the Expert 2K-FA Third Series reversible evidence path on `Rel.26_03_24_A`. Production promotion is separately bound to exact model, topology, an exact operator-declared actual firmware allowlist (`Rel.26_03_24_A` or `Rel.08_06_26_A`), and STANDBY/RX. The original firmware has the complete reversible D1 report; the newer firmware has Justin's separate read-only confirmation of the unchanged top setup grid and FAN NOISE page/cursor topology, not a second complete apply/restore report. Second Series setup recognition accepts either input number.

For unfamiliar layouts, discovery may enter an exact highlighted fan candidate only when the live legend says `[SET]:CONFIRM`; `[SET]:CHANGE` stops before SET because it may write immediately. Once a candidate fan page is captured, the wizard sends no selector, SAVE, or DISPLAY command and retains exclusive actuation ownership until the operator physically returns the amplifier to a newer checksum-valid STANDBY home screen.

`menu-report.v2` reports can be previewed and downloaded locally. They include normalized rows and exact raw 8x40 character/attribute grids for hardware receipts. Ordinary failure, abort, and expiry retain an explicitly incomplete diagnostic report; reconnect or model change still invalidates report access. Incomplete reports cannot be uploaded. Completed-report upload requires explicit consent and defaults to the public receive-only collector at `https://expert-amp-menu-reports.sociallabs.workers.dev/v1/menu-reports`, so testers do not need to configure an internal endpoint. Override it with `-menu-report-collector-url` or `EXPERT_AMP_MENU_REPORT_COLLECTOR_URL` for development or self-hosting; pass an empty command-line value to disable uploads. Common IP, URL, email, callsign, and serial-device patterns are redacted, session tokens are omitted, and operators must review the local preview before consenting to upload.

## Documentation

Start here:

- [Raspberry Pi install guide](docs/INSTALL_PI.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Display accessibility and passive capture](docs/DISPLAY_ACCESSIBILITY.md)
- [Protocol notes](docs/PROTOCOL.md)
- [Node-RED integration guide](docs/integrations/node-red.md)
- [Screenshot/reference index](docs/reference/screens/README.md)
- [Release readiness checklist](docs/release/READINESS.md)
- [Changelog](CHANGELOG.md)

## Build release artifacts

```bash
TARGETS="linux/arm64" VERSION="$(git describe --tags --always --dirty)" packaging/scripts/build-release.sh
```

The release helper injects build metadata into the server binary and copies the sample config plus systemd unit into `dist/`.

## License

MIT. See [LICENSE](LICENSE).
