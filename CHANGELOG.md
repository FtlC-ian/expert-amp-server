# Changelog

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
