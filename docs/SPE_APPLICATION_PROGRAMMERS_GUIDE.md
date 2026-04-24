# SPE Expert Application Programmer’s Guide, agent reference

Authoritative source: SPE’s Application Programmer’s Guide, Rev 1.1, for Expert 1.3K-FA, 1.5K-FA, and 2K-FA amplifiers.

This is a curated reference, not a raw transcription. It keeps the vendor-documented protocol details that matter for automation, status polling, band decoding, warnings, alarms, and the protocol-vs-display authority split.

## Authority and implementation guidance

The vendor documents the status poll and status fields as part of the protocol.

For this repository, treat the protocol status poll response as the machine-readable source of truth for automation.

Treat the front-panel display as a user-facing view that can differ in formatting or timing.

Complex operations, including settings, antenna presets, and firmware updates, are not documented as available through this protocol and must use SPE’s KTerm software.

## Transport and framing

Vendor-documented transport details:

- Physical link: USB or RS232, not both at the same time.
- RS232 DB9 pinout:
  - Pin 7, TX
  - Pin 8, RX
  - Pin 5, GND
- Serial format: 8-N-1.
- Speed: up to 115,200 baud, with autobaud support.

Packet framing, as documented:

- Host to amp sync bytes: `55 55 55`
- Amp to host sync bytes: `AA AA AA`
- Byte 3 is the payload length, excluding checksum.
- Payload follows the count byte.
- Checksum follows the payload.

Status-string framing (project interpretation of the PDF framing text):

- Response sync bytes: `AA AA AA`
- Count byte: `CNT = 0x43` (67 data characters)
- Data: `DATA[67]` (ASCII CSV status string)
- Checksum bytes: `CHK0`, `CHK1`
- Terminator: `CR LF`
- `CHK0 = SUM(DATA0..DATA66) % 256`
- `CHK1 = SUM(DATA0..DATA66) / 256`

## Command bytes

These are the vendor-documented command bytes.

| Command | Hex |
| --- | --- |
| INPUT | `01` |
| BAND - | `02` |
| BAND + | `03` |
| ANTENNA | `04` |
| L - | `05` |
| L + | `06` |
| C - | `07` |
| C + | `08` |
| TUNE | `09` |
| SWITCH OFF | `0A` |
| POWER | `0B` |
| DISPLAY | `0C` |
| OPERATE | `0D` |
| CAT | `0E` |
| LEFT ARROW | `0F` |
| RIGHT ARROW | `10` |
| SET | `11` |
| GET STATUS | `90` |
| BACKLIGHT ON | `82` |
| BACKLIGHT OFF | `83` |

## Status poll

The documented status request is `90`.

The response is a fixed ASCII CSV string, 19 fields, 67 characters total in the rendered example from the guide.

Rendered sample string from the PDF (non-canonical formatting):

`20K,S,R,x,1,00,1a,0r,L,0000, 0.00, 0.00, 0.0, 0.0, 33, 0, 0,N,N`

Real hardware capture seen from a live `55 55 55 01 90 90` poll on the FTDI control port:

`AA AA AA 43 2C 31 33 4B 2C 53 2C 52 2C 41 2C 32 2C 30 35 2C 34 62 2C 30 72 2C 4C 2C 30 30 30 30 2C 20 30 2E 30 30 2C 20 30 2E 30 30 2C 20 30 2E 30 2C 20 30 2E 30 2C 20 32 35 2C 30 30 30 2C 30 30 30 2C 4E 2C 4E 2C 3B 0D 2C 0D 0A`

Notes from that capture:
- the count byte is still `0x43`, and the 67 counted data bytes still checksum correctly
- this firmware/frame variant includes a leading comma in the counted data area
- the wire terminator after the checksum is `2C 0D 0A`, not just `0D 0A`
- after trimming that framing quirk, the payload still resolves to the same fixed 19-field status record from the guide

## Status fields

| # | Field | Meaning | Vendor-documented values |
| --- | --- | --- | --- |
| 1 | ID | Model identifier | `20K` for 2K-FA, `13K` for 1.3K-FA |
| 2 | Mode | Operating mode | `S` standby, `O` operate |
| 3 | State | RF state | `R` receive, `T` transmit |
| 4 | Bank | Bank selection | `A`, `B` on 1.3K-FA, `x` on 2K-FA |
| 5 | Input | Antenna input | `1` or `2` |
| 6 | Band | Band code | `00` through `10` for 2K-FA, `11` for 1.3K-FA |
| 7 | ATU status | ATU state plus antenna | first char is antenna number, second char indicates tuning/bypass/enable state |
| 8 | RX Ant | Receive-only antenna | `0r` default, or an antenna number when RX-only is set |
| 9 | Power Lvl | Power class | `L`, `M`, `H` |
| 10 | Pwr Out | Output power | Watts, `0000` in RX |
| 11 | SWR ATU | VSWR before ATU | formatted numeric value |
| 12 | SWR ANT | VSWR at antenna | formatted numeric value |
| 13 | V PA | PA supply voltage | `0.0` in RX, about `48.0` in operate |
| 14 | I PA | PA current | formatted numeric value |
| 15 | Temp Upr | Upper heatsink temp | temperature value |
| 16 | Temp Lwr | Lower heatsink temp | 2K-FA only, `000` on 1.3K-FA |
| 17 | Temp Cmb | Combiner temp | 2K-FA only, `000` on 1.3K-FA |
| 18 | Warnings | Warning code | see warning table below, `N` means none |
| 19 | Alarms | Alarm code | see alarm table below, `N` means none |

## Warning codes

| Code | Meaning |
| --- | --- |
| M | Alarm amplifier |
| A | No selected antenna |
| S | SWR antenna |
| B | No valid band |
| P | Power limit exceeded |
| O | Overheating |
| T | Combiner overheating |
| C | Combiner fault |
| Y | ATU not available |
| K | ATU bypassed |
| W | Tuning with no power |
| R | Power switch held by remote |
| N | No warnings |

## Alarm codes

| Code | Meaning |
| --- | --- |
| S | SWR exceeding limits |
| A | Amplifier protection |
| D | Input overdriving |
| H | Excess overheating |
| C | Combiner fault |
| N | No alarms |

## What is vendor-documented versus inferred

Vendor-documented:

- transport and baud/format details
- command bytes
- the `0x90` status poll
- the CSV status fields and their meanings
- band codes, warnings, and alarms
- the statement that KTerm is required for operations outside the documented protocol

Inferred or worth verifying against captures:

- the exact byte-level checksum behavior for every response type
- how strictly the 67-character example generalizes across firmware revisions
- whether framing quirks such as a leading CSV comma or an extra comma before CRLF vary across firmware/hardware revisions
- whether any status fields vary in spacing or formatting on real hardware
- whether additional structured responses exist beyond the documented status poll

## Notes for implementers

- Treat the status poll as authoritative over display text when both exist.
- Preserve raw codes as well as decoded text in any API surface.
- Do not invent unsupported control commands.
- If a capture disagrees with this reference, trust the capture and update the doc.

## Messy PDF spots

The PDF’s status-response framing section is a little hard to read in places, especially around sync/checksum notation and the rendered example string. The command table and field meanings are the useful parts, and those are clear enough to preserve here.
