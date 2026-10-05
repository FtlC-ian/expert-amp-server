# Release readiness checklist

This is a practical pre-release checklist for keeping the repo understandable and safe enough for technical outside testers without pretending the project is finished.

## Documentation shape

- Root `README.md`: short project overview, canonical API summary, safety/warranty note, and links into detailed docs.
- `docs/INSTALL_PI.md`: exact Pi/radio-host install path, service unit, sample config, version checks, and health/status sanity checks.
- `docs/PROTOCOL.md`: protocol authority rules and known hardware/protocol caveats.
- `docs/ARCHITECTURE.md`: current code/data-flow truth, not aspirational architecture.
- `docs/integrations/`: integration-specific guides such as Node-RED and future Thetis notes.
- `docs/reference/screens/`: screenshots used by docs/issues.

Keep docs in the repo so they stay synchronized with code, screenshots, and release artifacts.

## Screenshots

Current screenshot index: [`docs/reference/screens/README.md`](../reference/screens/README.md).

### First-party web UI

- Front Panel layout in normal standby state.
- Front Panel layout with display hidden/shown if both states are user-visible.
- Operator layout in normal standby state.
- Operator layout at narrow width around 655px, including default, menu-controls-hidden, and LCD-hidden compact variants.
- Operator layout around 940px with help mode enabled and Aux/manual controls expanded, showing the self-documenting mode.
- Operator layout with warnings/alarms preview or real warning state, clearly labeled as preview if synthetic.
- Operator layout with aux/manual controls expanded.
- Settings tab showing serial/config fields and restart language.
- API docs page at `/api/v1/docs`.

### Install/runtime verification

- `/api/v1/version` response from a release-built binary.
- `/healthz` response/header check if useful in docs.
- `/api/v1/status` showing serial/protocol-native recent-contact health on a live amp.
- `systemctl status expert-amp-server` or equivalent service-manager view, with host/private data redacted.

### Integrations

- Node-RED dashboard connected to Expert Amp Server, with Display open/operate colors and Display collapsed compact variants. Captures are in `docs/reference/screens/node-red/`.
- Thetis/gauge integration screenshots once that guide exists.
- Any real amp LCD vs rendered display comparison that helps calibrate expectations.

## Known caveats to keep visible

- Wake/power-on is an experimental DTR/RTS control-line path and needs broader hardware validation.
- Normal button endpoint intentionally blocks `back`, `on`, and `standby`.
- Some documented button actions are transport-real but still need broader user-visible hardware confirmation.
- Production fan profiles cover the promoted First Series 1.3K-FA, CONFIG-first Second Series 1.5K-FA, and exact firmware/topology-bound Third Series 2K-FA. Third Series requires verified STANDBY/RX and never sends DISPLAY or OPERATE/STANDBY; unsupported combinations remain blocked before SET. Newer Third Series firmware evidence is read-only topology confirmation, not a second reversible report.
- Menu Debug is disabled by default. Reviewed reversible value-change/SAVE tests cover First Series 1.3K-FA fan and two-bank A/B layouts, CONFIG-first Second Series 1.5K-FA fan, and the exact Third Series 2K-FA fan topology on `Rel.26_03_24_A` (NORMAL → QUIET → NORMAL in verified STANDBY/RX, without DISPLAY or OPERATE/STANDBY writes). Other model/layout/firmware combinations remain topology-only for this wizard; production support for Third Series `Rel.08_06_26_A` does not authorize a reversible Menu Debug test on that firmware.
- Fahrenheit-configured amplifiers must set `amplifierTemperatureUnit: "F"` before temperature monitoring or fan automation is enabled because the status protocol is unitless.
- `/healthz` is process liveness only; use `/api/v1/status` for serial/protocol health.
- The service intentionally assumes a trusted station LAN and usually listens on `:8088` for use from another shack PC; do not expose it directly to the public internet.
- HTTP/raw TCP have no authentication, raw TCP is unencrypted, and WebSocket origins are unrestricted. Network placement does not imply authorization; restrict access to trusted operators.
- Raw passthrough requires automatic fan control and overtemperature standby to be explicitly disarmed, not silently suspended. Neither tapped provenance nor recent contact grants safety authority.

## Reproducible release preparation

There is a local build helper, not a checked-in CI/release workflow. The v0.4.9 convention is a GitHub release with four raw binaries, a sample config, a systemd unit, and `SHA256SUMS`; no archives or detached signatures were published. `go.mod` requires Go 1.26.1 or newer. Freeze a clean reviewed commit first, record the actual toolchain, and run:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
test -z "$(gofmt -l $(git ls-files '*.go'))"
git diff --check
python3 -m json.tool internal/apidocs/openapi.json >/dev/null
go test ./internal/server -run 'Test(OpenAPI|RawPassthrough)' -count=1
```

The JSON parse is syntax validation, not a full OpenAPI validator. Also check all local `$ref` targets and canonical routes against current handlers. The spec is hand-maintained and embedded; there is no generation command in the repo. Keep `info.version` aligned with the release while preserving `/api/v1` route compatibility and historical fixture versions.

Use an empty dedicated output directory: the helper's checksum glob otherwise includes old artifacts already there. Explicitly set release metadata (the defaults are a Git describe version and channel `dev`):

```bash
VERSION=v0.5.0 COMMIT="$(git rev-parse --short HEAD)" \
BUILD_DATE="$(git show -s --format=%cI HEAD)" CHANNEL=release \
OUT_DIR="$PWD/dist/v0.5.0" packaging/scripts/build-release.sh
(cd dist/v0.5.0 && shasum -a 256 -c SHA256SUMS)
go version -m dist/v0.5.0/expert-amp-server_v0.5.0_darwin_arm64
```

Default artifacts for v0.5.0:

- `expert-amp-server_v0.5.0_linux_arm64`
- `expert-amp-server_v0.5.0_linux_arm_v7`
- `expert-amp-server_v0.5.0_linux_amd64`
- `expert-amp-server_v0.5.0_darwin_arm64`
- `expert-amp-server.service`
- `config.example.json`
- `SHA256SUMS` (covers the other six files, not itself)

Inspect metadata on all binaries; on a matching host, smoke-test only fixture/setup mode on loopback with an isolated config, checking `/api/v1/version`, `/healthz`, `/api/v1/docs`, and the served OpenAPI. Do not connect hardware or change a deployed service as part of offline release verification. Linux/systemd and real hardware installation checks need separately authorized host validation.

Preparation does not publish a tag, release, or assets. After independent review and authorization, land the reviewed tree, build/verify against the actual release commit (commit metadata changes if landing rewrites the SHA), publish only that artifact set, then download all assets and verify names, metadata, and checksums against the local receipts. Checksum verification is integrity evidence, not a signature or a hardware-safety guarantee.

## Ready enough for outside testers

- Install doc has been followed once from a clean-ish machine or release artifact directory.
- Version endpoint and service unit behavior have been verified from the installed binary.
- At least one current screenshot exists for Front Panel, Operator, Settings, and Node-RED. Thetis screenshots can follow when that integration guide is active.
- README links testers to install docs, protocol caveats, and known open issues.
- Open caveats are linked to issues instead of hidden in prose.
