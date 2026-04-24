# Expert Amp Server docs

Detailed project documentation lives here. Keep the root `README.md` short and approachable; put operational and protocol detail in this directory.

## Start here

- [`INSTALL_PI.md`](INSTALL_PI.md) — Raspberry Pi / radio-host install path, service wrapper, config, sanity checks, and caveats.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — package layout, server data flow, endpoint map, and wired/stubbed state.
- [`PROTOCOL.md`](PROTOCOL.md) — SPE protocol notes, display-frame assumptions, protocol-native status authority, and action transport caveats.
- [`integrations/node-red.md`](integrations/node-red.md) — Node-RED dashboard integration guide and flow notes.
- [`reference/screens/`](reference/screens/) — UI, Node-RED, and documentation screenshots.
- [`release/READINESS.md`](release/READINESS.md) — checklist for release/public handoff sanity checks.

## Screenshot/reference assets

Use [`reference/screens/`](reference/screens/) for screenshots and visual evidence that support docs or issues. Prefer focused captures with descriptive filenames over giant unsorted dumps.

Suggested subdirectories:

- `reference/screens/watch-panel/` — first-party Front Panel layout.
- `reference/screens/watch-operator/` — first-party Operator layout.
- `reference/screens/settings/` — Settings tab and first-run/config behavior.
- `reference/screens/node-red/` — Node-RED dashboard examples.
- `reference/screens/thetis/` — Thetis/gauge integration screenshots when that guide exists.
- `reference/screens/hardware/` — real amp/display comparison shots when useful.

Do not commit private credentials, serial numbers that should stay private, LAN-only hostnames you do not want public, or screenshots with unrelated browser/chat content.
