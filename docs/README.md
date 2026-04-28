# Expert Amp Server docs

This directory is the project documentation set for public testing and future release work. Keep the root `README.md` short enough to orient a new reader; put operational detail here.

## Start here

- [`INSTALL_PI.md`](INSTALL_PI.md) — repeatable Raspberry Pi / radio-host install path, service wrapper, config, sanity checks, and caveats.
- [`public-testing/PLAN.md`](public-testing/PLAN.md) — what needs to be checked before sharing builds with outside testers.
- [`PROTOCOL.md`](PROTOCOL.md) — SPE protocol notes, display-frame assumptions, protocol-native status authority, action transport caveats.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — package layout, server data flow, endpoint map, and current wired/stubbed state.
- [`integrations/node-red.md`](integrations/node-red.md) — Node-RED dashboard integration guide and flow notes.

## Screenshot/reference assets

Use [`reference/screens/`](reference/screens/) for screenshots and visual evidence that support docs, issues, or public testing notes. Prefer small focused captures with descriptive filenames over giant unsorted dumps.

Suggested subdirectories:

- `reference/screens/watch-panel/` — first-party Front Panel layout.
- `reference/screens/watch-operator/` — first-party Operator layout.
- `reference/screens/settings/` — Settings tab and first-run/config behavior.
- `reference/screens/node-red/` — Node-RED dashboard examples.
- `reference/screens/thetis/` — Thetis/gauge integration screenshots for #7.
- `reference/screens/hardware/` — real amp/display comparison shots when useful.

Do not commit private credentials, serial numbers that should stay private, LAN-only hostnames you do not want public, or screenshots with unrelated browser/chat content.
