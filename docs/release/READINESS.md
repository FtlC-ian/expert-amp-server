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
- `/healthz` is process liveness only; use `/api/v1/status` for serial/protocol health.
- The service intentionally assumes a trusted station LAN and usually listens on `:8088` for use from another shack PC; do not expose it directly to the public internet.

## Ready enough for outside testers

- Install doc has been followed once from a clean-ish machine or release artifact directory.
- Version endpoint and service unit behavior have been verified from the installed binary.
- At least one current screenshot exists for Front Panel, Operator, Settings, and Node-RED. Thetis screenshots can follow when that integration guide is active.
- README links testers to install docs, protocol caveats, and known open issues.
- Open caveats are linked to issues instead of hidden in prose.
