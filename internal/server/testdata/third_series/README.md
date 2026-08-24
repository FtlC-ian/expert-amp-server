# Expert 2K-FA Third Series fixtures

These four raw 8x40 `chars` and `attrs` captures were supplied by Justin
(AI5OS) from an Expert 2K-FA running firmware `Rel.26_03_24_A`:

<https://gist.github.com/w9fyi/ef874209c5ee43b69d6f786c318bf784>

They cover STANDBY/RX home, the setup grid, NORMAL-active/NORMAL-selected fan
state, and NORMAL-active/SAVE-selected fan state. They do not claim to cover
QUIET-active, a staged change, saved-home re-entry, or restored-home receipts.
Those states must come from the separately guarded hardware run; tests must not
synthesize them and label them as captures.

The `report_00acd527_*` fixtures are the 23 distinct exact raw states from
production D1 report `00acd5279f994baf497f58cf607f9efea6b770fbf3679beae301a88ce177b715`,
received 2026-08-16 from server `v0.4.7+pr31`. That complete reversible report
verified NORMAL to QUIET, SAVE/home, physical apply, re-entry, restoration to
NORMAL, and SAVE/home again on firmware `Rel.26_03_24_A`. The production
profile maps hardware QUIET to logical normal cooling and hardware NORMAL to
logical high cooling. It remains STANDBY/RX-only and never authorizes DISPLAY.
`report_00acd527_provenance.json` maps every fixture to the full report ID,
original evidence-array index, display generation, and analyzer fingerprint.
