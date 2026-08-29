# Changelog

## v0.4.7 - 2026-08-28

### Added

- Add a candidate-only Expert 2K-FA Third Series reporting workflow bound to the exact model, reviewed setup topology, and firmware `Rel.26_03_24_A`. The guarded test runs hardware NORMAL → QUIET → NORMAL only from verified STANDBY/RX, records raw LCD evidence, requires physical apply and restore confirmation, and never sends DISPLAY or changes OPERATE/STANDBY.
- Add narrowly scoped startup-verification recovery for the reviewed First Series 1.3K-FA and Second Series 1.5K-FA top-level setup grids. A failed verification may send DISPLAY once only from an exact eligible setup waypoint with fresh STANDBY/RX evidence; it remains failed and latched until explicitly cleared.
- Recognize protocol identifier `15T` as `EXPERT 1.5K TAURUS` without assuming or enabling any Taurus menu-navigation profile.
- Retain a bounded fan-policy diagnostic trace of up to 32 verified semantic LCD waypoints, plus one-shot DISPLAY recovery attempt and origin fields, without storing raw LCD content or changing navigation behavior.

### Fixed

- Keep unified `both` polling fair after delayed serial reads so status and display requests continue alternating without catch-up bursts or starvation.
- Preserve explicit `false` values for `serialAssertDTR` and `serialAssertRTS` across settings responses, config rewrites, unrelated settings saves, and restarts while retaining the existing `true` default for absent fields.
- Preserve fixed-column display telemetry values containing spaces, such as `80 m` and `90 F`, without shifting ANT, CAT, output-level, SWR, or temperature fields.
- Accept ordinary repeated LCD spaces in reviewed setup labels such as `RX  ANT` while continuing to reject changed labels, split highlights, controls, Unicode separators, and unsafe SAVE matches.
- Accept the reviewed Second Series setup topology on either INPUT 1 or INPUT 2 without weakening model or layout binding.
- Surface bounded collector validation errors, keep transient report loading and recovery retryable, improve terminal announcements and Menu Debug accessibility, and prevent cross-model, reconnect, stale-evidence, or ambiguous-screen data from authorizing a candidate transaction.

### Validation and scope

- The Third Series candidate completed one physically confirmed NORMAL → QUIET → NORMAL evidence run, but production 2K-FA fan control remains excluded pending the separate production-path hardware gate in draft PR #34.
- The First Series Expert 1.3K-FA and Second Series Expert 1.5K-FA remain the only production fan profiles on `main`.

## v0.4.6 - 2026-08-12

Built-in Menu Debug report collector release.

### Added

- Include the public receive-only Menu Debug collector URL in official builds so operators can explicitly upload completed, sanitized reports without configuring an environment variable.
- Keep upload opt-in through an explicit consent checkbox and one-shot action; incomplete reports remain local-only, and the collector has no amplifier-control route.
- Preserve the existing environment-variable and command-line overrides for development, self-hosting, or complete upload opt-out.

### Validation

- Verified the full Go suite, collector TypeScript and Vitest checks, live collector health, embedded build metadata, release assets, and SHA-256 checksums before publication.

## v0.4.5 - 2026-08-03

Expert 1.5K-FA production fan-control release.

### Added

- Promote the hardware-verified Expert 1.5K-FA Second Series CONFIG-first fan profile for display-verified NORMAL and CONTEST control.
- Retain production support for the First Series Expert 1.3K-FA while leaving F-KFA and unknown layouts blocked before value changes or SAVE.

### Safety hardening

- Bind every menu and OPERATE write to the exact live serial session, amplifier model, expected operating state, and RX status.
- Discard callbacks from retired serial sessions before they can affect navigation, recovery, passive SAVE evidence, or verified policy receipts.
- Bind persisted verified fan-policy receipts to the amplifier model and clear them after model or serial-session replacement.
- Fail closed if the connected amplifier changes, including between two otherwise supported models.

### Validation

- Promote the 1.5K profile from a complete, physically confirmed NORMAL → CONTEST → NORMAL guarded report.

## v0.4.4 - 2026-08-03

Complete fan-only Menu Debug report corrective release.

### Fixed

- Finalize the built-in wizard report immediately after successful fan restoration confirmation instead of silently continuing into unrelated bank discovery.
- Keep bank topology available only to API clients that deliberately request it.

### Safety

- Preserve one authorization and one newer display receipt per amplifier command, fail closed on TX or stale state, refuse incomplete uploads, and never blindly retry or restore OPERATE.

## v0.4.3 - 2026-08-02

Automatic guarded Menu Debug and retained diagnostics release.

### Changed

- One explicit **Start Guarded Test** now runs discovery and the frozen apply plan automatically while preserving one authorization and one newer matching LCD receipt per command.
- Positive physical candidate confirmation starts the separately guarded automatic restore; physical restored confirmation remains mandatory.
- The wizard shows the current transaction, command, expected screen, and display-evidence wait state instead of requiring a Continue click for every command.
- Reports now use `menu-report.v2`. Ordinary failure, abort, and expiry retain an explicitly incomplete local diagnostic report; incomplete reports cannot be uploaded.

### Safety hardening and fixes

- A single session runner spans discovery, apply, and restore with an atomic queued-work handoff, preventing overlapping runners and dropped restore starts.
- Any observed TX is latched immediately even if a later RX frame arrives before the runner polls again.
- Abort is serialized with final command dispatch and cancels queued runner work before returning.
- Transport errors stop immediately, and returned failure views now reflect the stored terminal state.
- Reconnect and model changes still invalidate the entire session and destroy all report evidence.
- The Node-RED guide explicitly rejects blind fixed command sequences, including the proposed 13-command Expert 1.5K-FA macro.

## v0.4.2 - 2026-08-01

Menu Debug identity-binding corrective release.

### Safety hardening and fixes

- Frozen reviewed Menu Debug plans now retain the exact amplifier model that selected the profile.
- Plan installation, apply/restore entry, and every planned action authorization fail closed if the live model changes after discovery.
- Protocol status and checksum-valid LCD evidence are bound to the live serial session that produced them; reconnects invalidate both until the replacement session supplies fresh evidence.
- Reviewed plans and one-write authorizations freeze that session generation, and the transport atomically refuses a different or status-unconfirmed port.
- A reconnect now invalidates every active Menu Debug session and any retained capability report, so replacement-session evidence cannot complete, contaminate, download, upload, or relabel prior-amplifier evidence.
- Sanitized reports now include the optional reviewed `setupTopology` classification used for candidate-profile selection.

## v0.4.1 - 2026-08-01

Evidence-selected fan-profile and topology-binding release.

### Added

- A candidate Expert 1.5K-FA Second Series Menu Debug profile based on its captured CONFIG-first SET-menu topology.
- Profile identity, setup topology, supported fan values, and production/candidate capabilities in Menu Debug reports and API responses.
- Cross-family regression coverage and a production First Series discovery-to-plan test.

### Safety hardening

- Production fan writes now require both the reported model and the exact promoted SET-menu topology; model names or similar labels alone cannot authorize a write.
- Only the hardware-verified First Series Expert 1.3K-FA profile is promoted for production fan control.
- The Second Series 1.5K-FA profile remains candidate-only until an operator physically verifies both NORMAL to CONTEST apply and restoration to NORMAL.
- Unknown, ambiguous, cross-paired, and F-KFA NORMAL/QUIET layouts remain topology-only and cannot change a value or SAVE; QUIET is never interpreted as high cooling.
- Setup-family identity and ordered waypoints remain bound through the complete reviewed transaction.

### Validation

- The First Series 1.3K-FA completed display-verified Menu Debug and production override NORMAL to CONTEST to NORMAL cycles, returning to STANDBY/RX with fan automation and manual override disabled.

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
