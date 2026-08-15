# Expert 2K-FA Third Series fixtures

These four raw 8x40 `chars` and `attrs` captures were supplied by Justin
(AI5OS) from an Expert 2K-FA running firmware `Rel.26_03_24_A`:

<https://gist.github.com/w9fyi/ef874209c5ee43b69d6f786c318bf784>

They cover STANDBY/RX home, the setup grid, NORMAL-active/NORMAL-selected fan
state, and NORMAL-active/SAVE-selected fan state. They do not claim to cover
QUIET-active, a staged change, saved-home re-entry, or restored-home receipts.
Those states must come from the separately guarded hardware run; tests must not
synthesize them and label them as captures.
