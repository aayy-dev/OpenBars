# Open Bars Connect for Windows

Connects a Windows PC to Open Bars on an iPhone, like **Connect** on a Mac or iPad.

How it works:

- **Pairing:** you pair once and tap **Allow** on the iPhone.
- **Connecting:** after that, whenever the PC is on that iPhone's Personal Hotspot, over Wi‑Fi or a USB cable, its traffic goes through an encrypted tunnel to Open Bars on the iPhone. Open Bars sends it out as the iPhone's own traffic.
- **Disconnecting:** the tunnel turns off by itself everywhere else.

Requires Windows 10 or 11 (x64 or ARM64) and the Microsoft Edge WebView2 Runtime, which Windows 11 includes.

## Install

1. Download `OpenBars-windows-<arch>-<version>.zip` and unzip it anywhere.
2. Run `OpenBars.exe` and allow the install prompt (administrator).

The installer:

- copies Open Bars to `C:\Program Files\Open Bars`;
- installs a small background service (**Open Bars Connect**) that runs the tunnel;
- adds Open Bars to the Start menu and to **Settings › Apps**;
- starts the tray icon at sign-in.

From a terminal: `OpenBars.exe install` (as administrator). To remove Open Bars: **Settings › Apps › Open Bars Connect › Uninstall**, or `OpenBars.exe uninstall`. Add `--keep-data` to keep the pairing.

The builds aren't code-signed yet, so SmartScreen may warn the first time. Choose **More info › Run anyway**.

## Using it

1. On the iPhone, turn on Personal Hotspot, open Open Bars and tap **Start**.
2. Join the iPhone's hotspot on the PC. The Wi‑Fi network is named after the iPhone.
3. Open Open Bars from the tray and choose **Pair with iPhone**, then tap **Allow** on the iPhone within 90 seconds.

The tray icon shows the state:

| Bars | Meaning |
| --- | --- |
| Green | Connected |
| Half green | Connecting |
| Orange | Problem (hover for details) |
| Gray | Waiting for the hotspot, or off |

**Diagnose** checks each link in order:

1. hotspot
2. Open Bars sharing on the iPhone
3. pairing
4. automatic connect
5. tunnel
6. DNS
7. internet

It stops at the first broken link and says what to do.

**Log** uses the same format as the iPhone and Mac apps, in this PC's time zone (or UTC). **Detailed logging** adds per-connection details for 30 minutes. **Export** saves a text file to Downloads for sharing.

## How it works

- `OpenBars.exe service` runs as LocalSystem. It finds the adapter on an iPhone hotspot: an address in `172.20.10.0/28` with `172.20.10.1` as its gateway. Then it asks Open Bars on the iPhone whether it still has this PC (`GET /api/pair?public_key=…`). This works without reading the Wi‑Fi name, which Windows 11 restricts to apps with location permission.
- If the iPhone confirms, the service brings up a Wintun adapter named **Open Bars**. On it, wireguard-go sends all IPv4 and IPv6 traffic, plus DNS, to the iPhone. The encrypted UDP is pinned to the hotspot adapter, so it can't loop into the tunnel. The adapter, its routes and its DNS disappear when the tunnel stops.
- The private key is created on this PC and never leaves it. It's stored with DPAPI for the service account only, in `%ProgramData%\Open Bars` (readable only by SYSTEM and Administrators), together with the pairing and the log.
- The tray app runs as you and talks to the service over the named pipe `\\.\pipe\OpenBarsConnect`, which only SYSTEM, Administrators and the signed-in user can open. Its window is a WebView2 page.

## Building

On macOS or Linux, with Go installed:

```sh
windows/build.sh 1.0.0          # dist/OpenBars-windows-{amd64,arm64}-1.0.0.zip
DEV=1 windows/build.sh dev      # console build: output shows up in terminals and over SSH
```

The script:

- downloads Wintun 0.14.1 from wintun.net and checks its SHA-256;
- embeds the icon and manifest with go-winres;
- writes `THIRD_PARTY_NOTICES.txt` from the modules actually linked;
- cross-compiles with `CGO_ENABLED=0`.

`go run ./tools/mkicons` redraws the tray and app icons.

Tests: `go test ./...` in `windows/` covers everything that isn't Windows-only: pairing, hotspot detection, the connect/disconnect state machine, logging and the pipe protocol. On Windows, `go test ./...` also checks DPAPI and reads the real adapter list.

For testing over SSH, run these with a `DEV=1` build:

- `OpenBars.exe service-debug` runs the service in the foreground. Set `OPENBARS_DATA` to a temporary folder to keep its files out of `%ProgramData%`.
- `OpenBars.exe status`, `logs` and `diagnose` query the running service.

## Known limits

- **DNS can leak.** Windows may also send DNS queries to the hotspot's resolver ("smart multi-homed name resolution"). The tunnel's adapter has the lowest metric, so its DNS is preferred, but there's no firewall-level DNS leak protection yet.
- **Device type.** The iPhone shows the PC as a generic device until its Open Bars accepts the `windows` device kind.
- **No real installer yet.** There's no MSI/MSIX and no code signing.

"WireGuard" and the "WireGuard" logo are registered trademarks of Jason A. Donenfeld.
