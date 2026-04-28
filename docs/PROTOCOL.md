# Expert Amp Server — Protocol Notes

This document captures what we currently understand about the SPE Expert amplifier binary protocol, how we decode it, and what remains uncertain or unverified.

Primary sources for protocol work are:
- captured behavior from the real amp
- the vendor `SPE_Application_Programmers_Guide.pdf` (especially section 5, "The String Status", pages 8 to 10 in the current PDF)
- the reverse-engineering notes in this repo

**Read this before touching the decoder, the character map, the attribute model, or anything that claims to know what the amp is saying.**

Captured behavior from the real amp wins over documents, notes, and reasonable-seeming assumptions. When something is a hypothesis rather than a confirmed observation, this document says so explicitly.

---

## Authority order and distinct concerns

For amp status, keep the authority order explicit:

1. **Protocol-native vendor status poll data is authoritative** when the documented status poll/response path is available.
2. **Display-derived telemetry is fallback data** for fields the status poll does not currently provide, or when protocol-native status is temporarily unavailable.
3. **Rendered display state and parsed screen text are never the canonical machine-readable contract.** They are useful for UI mirroring, debugging, and conservative fallback extraction.

Protocol work touches four separate things that are easy to conflate. Keep them distinct:

### 1. Rendered display state

**What it is:** The 8×40 grid of character and attribute bytes that describes what the amp's LCD is currently showing.

**How we get it:** A binary frame arrives over USB/serial. We decode it into `display.State{Chars[8][40], Attrs[40]}`. The state is purely presentational — it is what the screen looks like, not what the amp "is" in an operational sense.

**Where it lives:** `internal/display` (model), `internal/protocol` (decoder).

**Key point:** The display state does not directly contain values like "power = 350W." It contains characters and pixels that, when rendered, show "350W" somewhere on the screen. Telemetry extraction is a separate step.

---

### 2. Parsed text

**What it is:** A plain-text extraction of the display state — the characters read off the screen row by row, trimmed of trailing whitespace.

**How we get it:** `protocol.ScreenText(frame)` calls `StateFromFrame` then joins the rows.

**Example output from `real_home_status_frame.bin` (approximate):**
```
SOLID STATE
FULLY AUTOMATIC
STANDBY
...
```

**Where it lives:** `protocol.ScreenText` and `protocol.FrameMeta.ScreenText`.

**Key point:** Parsed text is useful for quick human review and debugging. It is not the same as telemetry — you would need to parse field positions and label patterns out of the text to extract structured values. We have not done that systematically yet.

---

### 3. Structured status and telemetry

**What it is:** Structured values extracted from the amp state, band, power output, SWR, TX state, alarms, ATU state, and related fields, in a form that API consumers can use directly without parsing LCD text themselves.

**Current status:** We now have two related structured surfaces:
- `GET /api/v1/status` is the canonical automation status surface.
- `GET /api/v1/telemetry` is the current runtime telemetry snapshot, still useful, but not the authority when protocol-native status poll data exists.

**Authoritative path:** the vendor Programmer's Guide explicitly documents a separate status poll command (`55 55 55 01 90 90`) and a fixed-length ASCII CSV status response frame (`AA AA AA 43 ...`) for monitoring. That documented status poll is the primary source of truth for machine-readable amp state.

**Display role:** display parsing still matters, but as a fallback and supplement. It is appropriate for rendered UI state, debugging, and filling gaps when the status payload does not yet provide a field or current implementation has not surfaced it yet.

**Current implementation reality:** the documented status response is parsed for `/api/v1/status`, but the exported contract is still intentionally conservative. We should not claim fields the implementation does not actually emit yet, even when the guide suggests they belong in the long-term model.

**Specific guidance from the documented status payload:**
- model number should come from the status payload identifier bytes when available
- standby vs operate should come from the status payload state byte when available
- fields documented on Programmer's Guide pages 9 and 10 should be represented in status updates where supported by the implementation
- band exposes both the raw status code and a decoded text form (`bandText`)
- warnings and alarms preserve raw status codes and also expose decoded text fields (`warningsText`, `alarmsText`)
- documented status meter fields should surface as first-class API fields instead of leaking through `notes`, using raw-plus-decoded or parsed-plus-display pairs where the guide/current captures justify them

**Where it lives:** `internal/api/types.go`, `internal/protocol/status.go`, `internal/runtime/status_state.go`, the HTTP handlers serving `/api/v1/status`, and the OpenAPI artifact served from `/api/v1/openapi.json`.

---

### 4. Action transport

**What it is:** The mechanism for sending button presses or other commands to the amp over the serial/USB connection.

**Current status:** `POST /api/v1/actions/button` is the canonical route for documented front-panel button commands, with `POST /api/actions/button` retained as a compatibility alias. Separately, `POST /api/v1/actions/wake` is the canonical route for the experimental amp wake/power-on path that toggles DTR/RTS on the FTDI serial control lines. More broadly, the canonical REST surface for machine-readable clients now lives under `/api/v1/...`; older non-v1 routes are compatibility aliases or legacy debug helpers. Supported document-backed button names are encoded and written to the live serial transport when one is configured. If no live button transport is available, the server returns `503` with `button transport unavailable`. Actions that remain intentionally blocked on the button endpoint (such as `back`, `on`, and `standby`) return `400`; `on` stays blocked there because wake is not honest to model as a normal front-panel button.

**What we know (hypothesis):** The amp almost certainly accepts button commands over the same USB/serial connection that display frames arrive on. The framing format for outbound commands has not been reverse-engineered yet.

**Where it lives:** `internal/api/types.go`, `internal/transport/buttons.go`, `internal/runtime/serial_source.go`, `internal/server/server.go`, `cmd/server/main.go`.

**Open question:** What is the wire format for button press commands? What are the valid button names and their byte values? Are there safety constraints (e.g., should TUNE only be sendable in specific states)?

---

## What we know about the display frame format

### Header

Every display frame we have captured starts with the same historical 8-byte stream prefix:

```
AA AA AA 6A 01 95 FE 01
```

For 371-byte GetLCD responses, the first seven bytes are the real fixed header (`AA AA AA 6A 01 95 FE`) and byte 7 is also the first byte of the inverted flag word. Current checked-in 371-byte captures both have that low flag byte as `0x01`, so they still match the historical 8-byte prefix used by the stream decoder.

`IsRadioDisplayFrame` accepts both the fully recognized GetLCD response shape and the older 8-byte-prefix display captures.

**Status: confirmed from current real captures, but stream boundary detection should eventually move from the historical 8-byte prefix to the stronger length/checksum shape if other flag values change byte 7.**

### GetLCD response layout and body offset

The 371-byte captures (`real_home_status_frame.bin` and `sample_display_frame.bin`) match the reported response to the `0x80` GetLCD request:

```
[0..2]     sync preamble: AA AA AA
[3..4]     little-endian payload length: 0x016A = 362 bytes
[5..6]     unknown/status bytes: 95 FE
[7..8]     2-byte flag word, inverted little-endian
[9..328]   320-byte display body: 8 rows × 40 cols, one byte per cell
[329..368] 40-byte packed attribute/highlight bitplane
[369..370] 16-bit little-endian checksum over bytes [7..368] (flags + LCD payload)
```

The display body starts at byte 9 for those frames. `LCDDataOffset` recognizes this length/checksum shape and returns 9. Live stream frames may be longer than 371 bytes when a status-poll response arrives before the next display prefix; the display parser still treats the leading 371 bytes as the GetLCD response and ignores the trailing bytes for LCD payload and checksum purposes. Older 299-byte menu/panel captures also decode correctly at byte 9 but do not include the full GetLCD length/checksum tail, so they do not expose parsed flags.

`GuessDisplayStart` remains as a diagnostic text-scoring heuristic only; it is not used for decoding.

**Status: layout confirmed for the 371-byte home capture and structurally matched by `sample_display_frame.bin`; `real_home_status_frame.bin` checksum validates. `sample_display_frame.bin` carries the same length/offset shape but its checksum does not match the same sum rule, so treat its trailing checksum/flag truth as suspect.**

### LCD flags

`LCDFlagsFromFrame` preserves the raw inverted little-endian flag word and the decoded value (`raw ^ 0xffff`) for 371-byte GetLCD responses. Current fixture values:

| File | Raw inverted flags | Decoded flags | Checksum | Screen evidence |
|---|---:|---:|---|---|
| `real_home_status_frame.bin` | `0xf801` | `0x07fe` | valid | Standby screen |
| `sample_display_frame.bin` | `0x3301` | `0xccfe` | invalid under the confirmed sum rule | sample/proto text says Standby |

Andrew G0RVM reported that the flag word includes front-panel LED state. Live captures on an Expert 1.3K-FA validated several decoded high bits. The API exposes these under `lcdFlags.leds` only when the GetLCD checksum is valid; canonical machine-readable status still comes from the protocol status poll first.

| LED/state | decoded bit | validation |
| --- | ---: | --- |
| OP / operate | `0x1000` | Captured standby → operate; only this bit changed. |
| SET / setup menu | `0x2000` | Captured standby → setup menu; only this bit changed. |
| TUNE | `0x4000` | Captured burst during tune timeout; only this bit changed. |
| TX | `0x0800` | Adjacent reported flag bit; still needs a safe live TX capture. |

These LED bits are intended for the amp-shaped Front Panel template because they mirror the physical panel indicators more directly than status fields. They are not a replacement for the richer `/api/v1/status` data.

For protocol work, start the server with `-lcd-flag-debug` to log checksum-valid changes in unknown decoded flag bits. The log includes the unknown mask, full decoded/raw words, known LED states, and the first display line. This is opt-in so normal installs do not spam logs.

### Character encoding

Each character cell is a single byte. `DecodeDisplayChar` maps byte values:

| Byte range | Decoded as |
|---|---|
| `0x00` | Space |
| `0x01`–`0x1F` | Letters A–Z (offset by 1: `0x01` → `A`, `0x02` → `B`, …) |
| `0x20`–`0x5F` | Direct ASCII (printable range: space through `_`) |
| `0x8D`, `0x8E`, `0x8F`, `0xA0`–`0xA3` | `0x80` (routed to custom glyph bank) |
| All others | `·` (placeholder, unknown/unmapped) |

**Status: partially confirmed.** The A–Z mapping and the direct ASCII range work on current fixtures. The upper-byte glyph codes (`0x8x`, `0xAx`) are placeholder mappings for LCD-specific symbols — they render something, but whether the rendered glyphs match the real hardware characters has not been verified.

### Attribute bytes

The 40 bytes after the 320 display cells are decoded as a column-major highlight bitplane: one byte per column, with bit `r` (LSB = row 0) marking row `r` in that column as highlighted/reverse-video.

**Status: supported by the current fixture/rendering work and regression tests for menu-style highlights, but broader model/firmware validation is still useful.**

---

## Display model

Once decoded, the display is an 8-row × 40-column grid of bytes. Each cell holds one character code. Rendering is deterministic: given the same `display.State` and `font.ROM`, the output is always the same.

The model is symmetric: the diff operation (`display.Compare`) checks both character and attribute bytes per cell, producing a list of changed cells. This is the foundation for efficient incremental UI updates once live ingest is wired.

---

## Fixture files

The `fixtures/` directory contains real binary frames captured from hardware. These are the ground truth for decoder development and regression testing.

### What's there

| File | Description | Size |
|---|---|---|
| `real_home_status_frame.bin` | Home/status screen | 371 bytes |
| `real_menu_frame.bin` | Menu screen | 299 bytes |
| `real_panel_frame.bin` | Panel/control screen | 299 bytes |
| `sample_display_frame.bin` | Another status screen capture | 371 bytes |

All four start with the known `AA AA AA 6A 01 95 FE 01` header.

### Rules for using fixtures

**Do not modify the fixture files.** They are captured hardware output. Modifying them breaks regression value.

**Do not add synthetic fixtures.** A constructed binary that "looks like" a frame is not a fixture — it is a test vector. Keep them separate.

**Do add new real captures.** If you have access to the amp and capture new frames, add them to `fixtures/` with a descriptive name. Document what the screen was showing at capture time in a comment in the fixture-loading code or a `fixtures/README.md`.

**Use fixtures for regression tests.** When changing the decoder, write tests that load these files and assert on `ScreenText` output or specific cell values. Do not rely on manual visual inspection of renders as the only verification.

**If a fixture produces unexpected output, investigate before changing the decoder.** The fixture may be revealing something the current decoder gets wrong. That is the fixture doing its job.

### How fixtures flow into the server

At startup, `cmd/server/main.go` loads all three named fixtures:

```go
fixtures := map[string]string{
    "home":  "fixtures/real_home_status_frame.bin",
    "menu":  "fixtures/real_menu_frame.bin",
    "panel": "fixtures/real_panel_frame.bin",
}
```

Each is decoded via `protocol.LoadFixtureState`. If decoding fails, the fixture is silently skipped and the demo state is used as fallback. A decode failure at startup should be treated as a regression if it appears after a decoder change.

---

## Open questions and known gaps

These are things we do not yet know. Do not paper over them with assumptions.

1. **LCD TX flag validation.** The LCD flag word now exposes validated OP, SET, and TUNE LED states. TX is mapped to the adjacent reported bit but still needs a safe live TX capture before treating it as fully confirmed across documentation and UI screenshots.

2. **Additional structured frames beyond the documented status poll.** We know the vendor-documented status poll exists. What we do not yet know is whether the amp also sends other structured non-display frames over the same serial connection that are worth decoding separately.

3. **Button command format.** What byte sequence does the amp expect for a button press? What framing wraps it?

4. **Glyph mapping accuracy.** The custom glyphs in `internal/font/custom.go` are project-authored approximations. Their visual accuracy compared to what the real hardware LCD shows has not been verified.

5. **Multiple display families.** `ROADMAP.md` mentions display family detection. Different Expert models or firmware revisions may have different frame structures. We have only one hardware source of captures so far.

6. **Frame boundary detection.** The current stream decoder uses repeated display prefixes as boundaries, so a live display frame can include trailing bytes from an intervening status-poll response. The display parser tolerates this for the GetLCD layout, but the 371-byte length/checksum shape gives us a stronger future extraction boundary if we want cleaner raw-frame diagnostics.

7. **Upper-byte character codes.** Byte values above `0x5F` that are not in the known special-glyph set decode to `·`. Some of these likely correspond to real LCD symbols we have not mapped yet.

---

## What to do when captures disagree with code

This is the expected state. The code was written from partial information.

When real captured frames produce wrong output:

1. Keep the fixture. It is now evidence.
2. Document what the frame shows on real hardware (photo or description).
3. Update the decoder to match, not the fixture to match the decoder.
4. Update this document to reflect the new understanding, including what was wrong before.
5. Add a regression test so the fix is permanent.

**Do not guess.** If you cannot determine the correct behavior from available captures, document the uncertainty here and flag it for the next capture session.
