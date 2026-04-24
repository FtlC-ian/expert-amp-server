# Raspberry Pi install notes

These notes describe the current known-good shape for running `expert-amp-server` on a Raspberry Pi or similar Linux host near the SPE Expert amplifier. This is not a polished public installer yet; it is the handoff path to make the current deployment repeatable.

## Assumptions

- The amp is connected to the Pi with a USB cable to the amp's built-in USB Type-B control port. Do not use the separate CAT radio ports; those are for radio CAT connections, not this server.
- The Pi is on the same trusted LAN as the browser, Node-RED, or other clients.
- The service is reachable only from the trusted station LAN, VPN, firewall, or reverse proxy you control. It is not exposed directly to the public internet.
- You have a built Linux binary named `expert-amp-server` for the Pi architecture.
- You have a persistent config file path, for example:

```text
/var/lib/expert-amp-server/config.json
```

Use a path appropriate for the target host; do not rely on the development default `config/expert-amp-server.json` for a service install. A starter config lives at [`packaging/config/config.example.json`](../packaging/config/config.example.json).
The default service examples listen on `:8088` so another shack PC can open the UI at `http://radio-host:8088/`. That is intentional. This is local station-control software, not an internet-facing web application. If you need remote access, put it behind your own VPN/firewall/reverse proxy rather than exposing the process directly.


## Build a Pi binary

From a development machine, the repeatable release helper is:

```bash
VERSION=$(git describe --tags --always) CHANNEL=dev packaging/scripts/build-release.sh
```

By default it writes artifacts to `dist/` for Linux ARM64, Linux ARMv7, Linux AMD64, and macOS ARM64. Override `TARGETS` or `OUT_DIR` when needed:

```bash
TARGETS="linux/arm64" OUT_DIR=/tmp/expert-amp-release packaging/scripts/build-release.sh
```

For one-off manual builds, use Go directly:

```bash
GOOS=linux GOARCH=arm64 go build -o /tmp/expert-amp-server-linux-arm64 ./cmd/server
```

For 32-bit Pi targets, use `GOOS=linux GOARCH=arm GOARM=7`. The production box used during development is 64-bit ARM.

## Copy to the Pi

Example:

```bash
scp /tmp/expert-amp-server-linux-arm64 pi-user@radio-host:/home/pi-user/expert-amp-server.upload
ssh pi-user@radio-host 'mv /home/pi-user/expert-amp-server.upload /home/pi-user/expert-amp-server && chmod +x /home/pi-user/expert-amp-server'
```

## Configure USB control-port access

Plug the Pi/Linux host into the amp's built-in USB Type-B control port. Linux exposes that USB connection as a serial device, so the app still calls it `serialPort` in config and API fields. Do not plug this server into the amp's separate CAT radio ports; those are distinct radio-control serial ports.

Set the exposed serial device path in the JSON config or in the Settings tab after first launch. Prefer stable `/dev/serial/by-id/...` paths rather than `/dev/ttyUSB0` because USB device order can change.

Example Linux device path for the amp USB control port:

```text
/dev/serial/by-id/usb-FTDI_FT232R_USB_UART_XXXXXXXX-if00-port0
```

The service user must be able to open the USB control-port serial device. On many Linux systems that means adding the user to the `dialout` group or running under a service account that already has serial permissions.

Key config fields for a live amp setup are represented in [`packaging/config/config.example.json`](../packaging/config/config.example.json). Copy it, then edit `serialPort` for the target host's amp USB control-port device.

## Run manually

```bash
/home/pi-user/expert-amp-server -config /home/pi-user/expert-amp-server-config.json
```

The release helper injects version metadata automatically. Local/dev builds report `dev`; that is fine for hand testing, but public test binaries should carry a useful version or commit.

Then open:

```text
http://radio-host:8088/
```

Useful sanity checks:

```bash
curl http://127.0.0.1:8088/api/v1/status
curl http://127.0.0.1:8088/api/v1/runtime/snapshot
curl http://127.0.0.1:8088/api/v1/settings
curl http://127.0.0.1:8088/api/v1/version
```

A healthy live setup should report a serial source, recent contact, and protocol-native status when the status poll path is working.

`/healthz` returns `ok` and HTTP 200 when the process is running. It also includes an `X-Expert-Amp-Version` header. It does not confirm serial connection or data flow; use `/api/v1/status` for that.

## Service wrapper expectation

The app can request a clean runtime restart through the UI/API, but it does not install or manage the host service itself. A real install should run the binary under a host supervisor such as `systemd`, Home Assistant add-on supervision, Docker, or another local service manager.

The repo includes a starter systemd unit at [`packaging/systemd/expert-amp-server.service`](../packaging/systemd/expert-amp-server.service). It is intentionally conservative and assumes:

- binary: `/usr/local/bin/expert-amp-server`
- config: `/var/lib/expert-amp-server/config.json`
- service user/group: `expert-amp`
- serial access through supplementary group `dialout`

Adjust those paths and names for the target machine before installing.

### Minimal systemd install

Create the service user and config directory:

```bash
sudo useradd --system --home /var/lib/expert-amp-server --shell /usr/sbin/nologin expert-amp
sudo mkdir -p /var/lib/expert-amp-server
sudo chown expert-amp:expert-amp /var/lib/expert-amp-server
sudo usermod -a -G dialout expert-amp
```

Install the binary, config, and unit:

```bash
sudo install -m 0755 expert-amp-server /usr/local/bin/expert-amp-server
sudo install -m 0644 expert-amp-server.service /etc/systemd/system/expert-amp-server.service
sudo cp config.example.json /var/lib/expert-amp-server/config.json
sudo chown expert-amp:expert-amp /var/lib/expert-amp-server/config.json
sudo systemctl daemon-reload
sudo systemctl enable --now expert-amp-server
sudo systemctl status expert-amp-server
```

Use the service manager's logs for startup/debug output, for example:

```bash
journalctl -u expert-amp-server -f
```

The unit uses `Restart=always`, so `POST /api/v1/runtime/restart` causes the process to exit cleanly and systemd brings it back automatically. With the default `RestartSec=5`, the UI/API may be unavailable for a few seconds during restart; that is expected. For an intentional shutdown, use `sudo systemctl stop expert-amp-server`.

Common gotchas from the first radio-host smoke test:

- If another manually-started `expert-amp-server` process is already listening on `:8088`, stop it before enabling the systemd unit.
- Preserve or copy the known-good config into `/var/lib/expert-amp-server/config.json`; otherwise the service may start in setup/fixture mode instead of talking to the amp.
- Make sure the service user can open the FTDI serial device. If status stays fixture-derived or serial contact is false, check group membership and the `/dev/serial/by-id/...` path first.
- `RestartSec=5` means a clean restart request is not instant. Give systemd a few seconds before deciding the service failed.
- `/healthz` only proves the process is up. Use `/api/v1/status` to confirm live serial and protocol-native contact.

## Button and wake caveats

Normal front-panel button actions use:

```text
POST /api/v1/actions/button
```

`POST /api/v1/actions/wake` exposes the experimental DTR/RTS control-line wake path. It is separate from `POST /api/v1/actions/button` because wake is not a document-backed front-panel button frame.

Do not treat `on` as a normal button command. The current implementation keeps `back`, `on`, and `standby` blocked on the normal button endpoint. Hardware wake behavior still needs broader confirmation across real amps and cabling setups.

## Install handoff checklist

Before handing this install path to another user, verify:

- binary starts under the intended service manager
- `/api/v1/version` shows the expected release/build metadata
- config file path is explicit and persistent
- serial path uses `/dev/serial/by-id/...`
- service user can access the serial device
- Watch UI loads from another machine on the LAN
- `/api/v1/status` reports recent serial contact
- display mirror updates
- Operator layout shows sane power/SWR/band/antenna/input state
- normal button actions return success only when the serial transport is actually available
- wake behavior is documented honestly as experimental and hardware-dependent
