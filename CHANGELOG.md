# Changelog

## v0.4.0 - 2026-07-31

Guarded menu discovery, compatibility reporting, and serial recovery release.

### Added

- A disabled-by-default, 10-minute Menu Debug & Reporting session in Settings with memory-only tokens and revision-protected actions.
- Reviewed reversible NORMAL/CONTEST fan and Bank A/B test profiles for the confirmed First Series Expert 1.3K-FA layouts.
- Topology-only capture for unrecognized models and layouts; unknown screens never inherit a value-changing or SAVE profile.
- Sanitized `menu-report.v1` preview/download plus explicit one-shot upload to a separately configured receive-only HTTPS collector.
- Serial reconnect diagnostics and coordinated wake/reconnect handling.

### Safety hardening and fixes

- Every wizard write requires fresh protocol STANDBY/RX and a newer checksum-valid LCD receipt; the server sends at most one reviewed command before waiting for new evidence.
- TX, stale evidence, unexpected screens, timeout, or safety preemption stop the wizard fail-closed. It never performs blind menu recovery or restores OPERATE.
- A completed, display-verified Normal fan override can be cleared while arming the wizard without sending an amplifier command. CONTEST, pending, failed, or uncertain state still blocks arming.
- Added server-only recovery for failed fan transactions after the operator returns the amplifier to a fresh STANDBY/RX home screen.
- Fixed hidden verification controls, repeated LCD polls during actions, fan-selector advancement, and Bank SAVE confirmation handling.
- Serial write timeouts now retire the uncertain serial session and reconnect instead of leaving it reusable.
- Menu reports are isolated to the currently armed session, hostname-shaped firmware metadata is rejected, and the uploader refuses collector redirects.
- Failed fan recovery now remains latched and retains its actuation lease if the cleared state cannot be persisted.

### Known caveats

- Menu Debug remains experimental and disabled by default. Other model/layout combinations are topology-only until explicitly reviewed.
- The reviewed bank profile requires the exact two-bank A/B layout with Bank A active at the start.
- Do not transmit or enable OPERATE during a session. Sampled RX gates cannot eliminate the physical race before an unexpected PTT event.
- Session tokens expire after 10 minutes and are lost on page reload or server restart.
- Upload is available only when an HTTPS collector endpoint is configured; local JSON download remains available without it.
- The HTTP service remains unauthenticated and belongs only on a trusted station LAN or VPN.

## v0.3.1 - 2026-07-30

Corrective documentation and Node-RED integration release.

### Fixed

- Allowed the checked-in Node-RED Backlight On/Off tiles through the shared action router instead of silently rejecting their documented action names.
- Corrected serial guidance: `115200` remains the server default and vendor-documented maximum; `57600` is a confirmed troubleshooting fallback on one field-tested 2K-FA, not a model-wide requirement.

## v0.3.0 - 2026-07-30

Fan control, temperature safety, and cross-model field-test release.

### Added

- Display-verified manual Fan Boost, Fan Normal, Fan Auto, and EEPROM-backed Verify operations.
- Optional timed Fan Boost, with the timer starting only after CONTEST mode is verified.
- Disabled-by-default automatic fan cooling with configurable Celsius thresholds and hysteresis.
- Separately armed, state-gated overtemperature standby protection.
- Persistent fan-policy receipts that remain explicitly stale until live verification after restart.
- Canonical temperature-unit handling for amplifiers configured in Celsius or Fahrenheit.
- Direct vendor-documented backlight on/off actions.
- Stable Node-RED mappings for power level, power watts, SWR, PA voltage/current, and temperature.

### Changed

- Fan transactions temporarily enter verified STANDBY when required, pause all writes during TX, and restore OPERATE only when the controller owned the transition.
- Serial button and wake fallbacks now honor the configured baud rate.
- One 2K-FA installation is documented as field-confirmed for core server operation at the `57600` troubleshooting fallback; fan-menu compatibility remains experimental.
- The OpenAPI document now reflects the v0.3.0 API surface.

### Fixed

- Fixed Node-RED arc gauges that were present but never wired to status data.
- Fixed ANT SWR incorrectly mirroring ATU SWR even when `antennaSwr` was available.
- Fixed Backlight On and Backlight Off cycling the display page instead of sending their documented commands.
- Fixed Fahrenheit-configured amplifiers being interpreted as Celsius by monitoring and fan thresholds.
- Fixed brief `STORING DATA!` screens being missed between display polls after a verified SAVE.

### Safety and compatibility notes

- Fan control is hardware-confirmed on a First Series Expert 1.3K-FA. Test manual Fan Boost before enabling automatic control on another model.
- Set `amplifierTemperatureUnit` to match the amplifier SET menu before enabling temperature-based features; the protocol itself is unitless.
- Automatic fan cooling and overtemperature standby remain disabled unless explicitly enabled.
- The server is for a trusted station LAN and has no built-in authentication.

## v0.2.0 - 2026-04-28

Second public release, focused on real field-test feedback, serial polling reliability, and operator UI polish.

### Added

- Unified serial polling mode with a single polling interval and deterministic display/status scheduling.
- Station label settings for the built-in UI:
  - panel model / callsign label
  - input labels for inputs 1-2
  - antenna labels for antenna ports 1-6
- LCD flag diagnostics from GetLCD display frames.
- Front Panel LEDs driven from checksum-valid LCD flag data.
- Operator view SET/Menu and TUNE indicators driven from checksum-valid display websocket LED flags.
- Support for SPE Expert 1.5K-FA field reports in README wording.

### Changed

- Default polling cadence now uses the unified polling model rather than separate display/status clocks.
- Display websocket refreshes are quieter and avoid repeated no-op refreshes when frame metadata is value-identical.
- Node-RED example flow and docs were tightened after clean-import testing.
- Routine snapshot update logging is quieter; poll errors still log.
- Front-panel model fallback no longer lies as `1.3K`; it now falls back to generic `SPE` unless a model or station label is known.

### Fixed

- Fixed interleaved display/status serial polling where display decoding could swallow trailing status responses.
- Fixed display refresh churn caused by value-identical LCD flag pointers comparing unequal.
- Fixed hardcoded 1.3K front-panel label behavior.
- Fixed manual front-panel changes not reliably appearing during early field testing on newer builds.
- Fixed checked-in Node-RED `ui_level` widget config so clean imports no longer crash.
- Fixed Node-RED Power On routing so it uses the experimental wake path rather than pretending to be a normal button action.

### Known caveats

- TX LCD flag mapping still needs a safe live capture before it should be treated as fully proven.
- Fan and bank menu traversal are not implemented as normal server operations; they need display-verified state-machine design before being exposed.
- Overtemperature protection policy is still future work.
- The HTTP API is intended for a trusted station LAN, not direct public internet exposure.

## v0.1.0 - 2026-04-24

Initial public release.

### Included

- Trusted-LAN web UI and API for SPE Expert amplifiers.
- Browser Front Panel and Operator layouts.
- SPE LCD display mirroring.
- Protocol-native status polling where available, with display-derived fallback.
- Documented HTTP/WebSocket API surfaces.
- Node-RED example dashboard based on the WB2WGH SPE Linear flow style.
- Raspberry Pi / Linux systemd install notes.
- Release build helper and sample config.
