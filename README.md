# Open Bars

<img width="108" height="108" alt="Open Bars icon" src="docs/images/icon.png" />

Open Bars shares an iPhone's cellular connection with your other devices over an encrypted tunnel, using [wireguard-go](https://git.zx2c4.com/wireguard-go). Devices on the iPhone's Personal Hotspot send their traffic into the tunnel, and Open Bars on the iPhone re-opens every connection from the phone itself, so it goes out as the iPhone's own traffic.

> [!WARNING]
> **Early pre-release.** Hotspot sharing has only been tested on one iPhone so far. Expect rough edges, and please [open an issue](https://github.com/aayy-dev/OpenBars/issues) if something doesn't work on yours.

<p align="center">
  <img src="docs/images/share-sharing.png" width="250" alt="Share tab while sharing: live speed chart and paired devices">
  <img src="docs/images/share-usage.png" width="250" alt="Share tab when not sharing: devices and today's usage">
  <img src="docs/images/connect-setup.png" width="250" alt="Connect tab: three setup steps">
</p>

One app runs on iPhone, iPad and Mac:

- **Share** (the iPhone with Personal Hotspot on) runs the tunnel server.
- **Connect** (a Mac, iPad or another iPhone on that hotspot) pairs with the iPhone once, then sets up a VPN that turns itself on whenever the device joins that hotspot and off everywhere else.

Windows, Android and Linux devices use the standard WireGuard app with a config from the iPhone. Open Bars apps for Android and Windows are [planned](#plans).

Requires iOS 17 or later (Control Center controls and widgets need iOS 18) and macOS 15 or later.

## Install

Builds are on the [Releases](https://github.com/aayy-dev/OpenBars/releases) page. They're unsigned, so you sign them yourself.

**Building with Xcode is the only way to get every feature.** Background sharing, the Connect VPN, and the Control Center controls and widgets need the Network Extension and App Groups capabilities. Those require a paid Apple Developer account and an App ID registered for this app. See [Building](#building).

### iPhone and iPad: sideloading (e.g. Feather)

1. On the release, long-press `OpenBars-<version>.ipa` (CI builds are `OpenBars-<version>-build<N>.ipa`) and copy the link.
2. In [Feather](https://github.com/khcrysalis/Feather): **Library › + › Import from URL**, paste the link, then sign it with your certificate and install.

What works when sideloaded depends on your certificate's provisioning profile. Profiles from signing services are usually shared by many apps, so they can't carry those capabilities. On such a certificate, expect:

- **Works:** sharing while Open Bars is open, pairing requests, adding WireGuard-app devices, usage history, the Personal Hotspot shortcut.
- **Doesn't work:** **Keep sharing in the background**, along with the Control Center, widget, Siri and Shortcuts actions that rely on it, and the **Connect** tab's VPN (use the WireGuard app on the connecting device instead). Controls and widgets also can't show whether sharing is on.

### Mac

Build it with Xcode (see [Building](#building)). macOS allows a VPN app extension only in App Store builds and in builds signed with a development profile, so the release's unsigned Mac app can't add the VPN.

`OpenBars-<version>-macOS.zip` is included for completeness. To open it, run `xattr -dr com.apple.quarantine "Open Bars.app"` and `codesign --force --deep --sign - "Open Bars.app"`. It can pair, but not connect. Without a developer account, use the WireGuard app for Mac with a config from the iPhone (see **Other devices** under [Using it](#using-it)).

## Using it

**On the iPhone that shares**

1. Turn on Personal Hotspot, open Open Bars and tap **Start**. Allow Local Network access when asked.
2. By default sharing runs inside the app, so keep it on screen (the display stays awake while sharing). To lock the iPhone or switch apps, turn on **Settings › Keep sharing in the background**. Sharing then runs as a system VPN component that iOS keeps alive. It carries none of the iPhone's own traffic, but iOS runs one VPN at a time, so the iPhone can't use another VPN app while sharing this way.
3. Optional: **Settings › Personal Hotspot**. Apps can't switch on Personal Hotspot, but Shortcuts can. Add the "Open Bars Hotspot" shortcut from there, and Start turns the hotspot on too (Open Bars hops to Shortcuts for a moment and back).

Other ways to start and stop (iOS 18 or later):

- **Control Center:** **Open Bars Sharing** turns sharing on or off without opening the app (it runs in the background). **Open Bars + Hotspot** turns the hotspot and sharing on together, or off together, opening Open Bars for a moment. Both are highlighted while sharing.
- **Widgets, Siri and Shortcuts:** a Home Screen widget, and the actions **Start Sharing**, **Stop Sharing** and **Share with Hotspot**.

**On a Mac, iPad or iPhone that connects**

1. Join the iPhone's hotspot and open Open Bars › **Connect**.
2. Tap **Pair with iPhone**, then **Allow** on the iPhone. The device makes its own key pair; only the public key goes to the iPhone.
3. Approve adding the VPN configuration. From then on the "Open Bars" VPN connects by itself on that hotspot and stays off on other networks. On a Mac it also connects over a USB cable. **Diagnose** walks through each step if something doesn't work.

On a Mac, Open Bars lives in the menu bar once it's set up. **Settings › Menu Bar & Dock** chooses where it shows.

**Other devices (WireGuard app)**

In Share, tap **Add**, create a device under **With the WireGuard app**, then scan its QR code with the WireGuard app. On a laptop, open `http://172.20.10.1:8080` while on the hotspot and download its `.conf`.

## How it works

- The iPhone runs [wireguard-go](https://git.zx2c4.com/wireguard-go) with a userspace TCP/IP stack ([gVisor](https://gvisor.dev)) instead of a network interface. Each TCP connection and UDP flow from a device is answered locally and opened again from a socket on the iPhone, pinned to the cellular interface. If a VPN app is running on the iPhone (in-app sharing only), traffic follows that VPN instead.
- On IPv6-only carriers, IPv4 destinations are reached through the carrier's NAT64.
- Each device gets an IPv4 and an IPv6 address inside the tunnel, and its DNS goes to the iPhone, so none of its traffic leaves through the hotspot directly.
- Background sharing is a packet tunnel extension with no routes: it only keeps the server running while the app is closed.
- Connect is a Network Extension packet tunnel running wireguard-go, with on-demand rules matching the hotspot's Wi‑Fi name (the iPhone's name).
- Pairing is a small HTTP API on the iPhone (`/api/pair`), answered only after someone taps Allow there.

## Building

Requirements: macOS with Xcode 26 or later, Go (see `ios-app/core/go.mod`), [gomobile](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile), [XcodeGen](https://github.com/yonaskolb/XcodeGen) and a paid Apple Developer account (Network Extensions need one).

```sh
go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest
gomobile init

cd ios-app/core
gomobile bind -target=ios,iossimulator -iosversion=17.0 -ldflags="-s -w" \
  -o ../OpenBars/Frameworks/WGCore.xcframework ./wgcore
gomobile bind -target=ios,iossimulator,macos -iosversion=17.0 -macosversion=15.0 -ldflags="-s -w" \
  -o ../OpenBars/Frameworks/WGClient.xcframework ./wgclient
```

Bundle IDs and the app group (`dev.aayy.openbars…`, `group.dev.aayy.openbars`) belong to one developer team, so use your own prefix:

```sh
cd ios-app
grep -rlI 'dev\.aayy' OpenBars tools --exclude-dir=OpenBars.xcodeproj --exclude-dir=Frameworks \
  | xargs sed -i '' 's/dev\.aayy/com.yourname/g'
```

If you want the bundled "Share with Open Bars" shortcut, regenerate it for your bundle ID and team with `tools/make_shortcuts.py`, then sign it with `shortcuts sign --mode anyone`.

Then:

```sh
cd OpenBars
xcodegen generate
open OpenBars.xcodeproj
```

In Xcode, set your team on the `OpenBars` and `OpenBarsMac` targets and their extensions, then build and run on your devices. Xcode registers the App IDs. If signing fails on the app group, register `group.<your prefix>.openbars` under Certificates, Identifiers & Profiles and enable it on the app, `.tunnel` and `.widgets` IDs. The engine's tests run with `go test ./...` in `ios-app/core`.

The GitHub Actions workflow (`.github/workflows/ios-app.yml`) builds an unsigned `.ipa` on each push.

## Layout

| Path | What |
| --- | --- |
| `ios-app/OpenBars/Sources` | iPhone app and Share tab |
| `ios-app/OpenBars/Client` | Connect tab (shared by iOS and macOS) |
| `ios-app/OpenBars/Shared` | Sharing state, App Intents and tunnel control (shared with the extensions) |
| `ios-app/OpenBars/Connect` | Connect's tunnel extension (wireguard-go) |
| `ios-app/OpenBars/Tunnel` | Background-sharing extension (no routes) |
| `ios-app/OpenBars/Widgets` | Control Center controls and Home Screen widgets |
| `ios-app/OpenBars/Mac` | Mac app (window and menu bar) |
| `ios-app/OpenBars/Resources` | App icon (`AppIcon.icon` for iOS/macOS 26+, PNGs for older) and `Licenses.json` |
| `ios-app/core/engine` | The sharing server (wireguard-go, TCP/UDP forwarding, DNS, pairing, usage, config page) |
| `ios-app/core/wgcore`, `wgclient` | gomobile APIs for the iPhone app and the Connect extension |
| `ios-app/tools` | Icon, shortcut and license-list generators |

## Known limits

- Some carriers still cap upload speed for the whole phone while any device is on the hotspot. Open Bars can't change that.
- Pairing needs Open Bars open on the iPhone, so someone can tap Allow.
- Turning on Personal Hotspot goes through Shortcuts, so Open Bars briefly switches to Shortcuts and back.

## Plans

- **Connect for Android and Windows:** small Open Bars apps that pair with the iPhone the same way (tap Allow on the iPhone) and turn the tunnel on by themselves on its hotspot, like Connect on Apple devices. Until then, these devices use the WireGuard app.
- **Sharing from Android:** exploring whether the Share side can run on an Android phone too, so its mobile data can be shared the same way.

## License

Open Bars is released under the [MIT License](LICENSE).

The app lists the open-source software it ships with (wireguard-go, gVisor, Go and the `golang.org/x` packages) and their licenses under **Settings › About › Open-Source Licenses**. After changing `ios-app/core/go.mod`, regenerate that list with `python3 ios-app/tools/make_licenses.py`.

"WireGuard" and the "WireGuard" logo are registered trademarks of Jason A. Donenfeld.
