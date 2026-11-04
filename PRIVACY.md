# Privacy Policy

_Last updated: November 4, 2026_

This policy covers Open Bars for iPhone, iPad and Mac (including Open Bars: Hotspot Sharing on the App Store and TestFlight) and Open Bars for Windows. Open Bars is open source, so everything below can be checked in [the code](https://github.com/aayy-dev/OpenBars).

## In short

**Open Bars doesn't collect any data.** There are no accounts, analytics, ads or trackers, and no Open Bars servers. Nothing is sent to the developer. Open Bars doesn't sell, use or share any data with anyone, for any purpose.

## What stays on your devices

Open Bars stores what it needs to work on your own devices, and nowhere else:

- **On the iPhone that shares:**
  - its tunnel key;
  - your settings;
  - the devices you've paired or added: their names, device types and public keys, plus the full configs of devices added for the WireGuard app;
  - devices you removed: the same, kept so that if one pairs again, its data limits and usage carry on;
  - how much data each device used, by hour and day, and in total;
  - data limits you set, extra time or data you give, and your devices' requests for more, including any message written with them;
  - the log.
- **On a device that connects** (Mac, iPad, iPhone or Windows PC):
  - its own key pair. The private key is created on the device and never leaves it. Apple devices keep it in the Keychain; Windows keeps it encrypted for the Open Bars service only.
  - the pairing: the iPhone's name, its public key and the tunnel addresses;
  - the log.
- **Logs** record events and errors, such as when sharing starts, a device connects, or a check fails. They can include device names and local network addresses.
  - **Detailed logging** is off by default and turns itself off after 30 minutes. While it's on, the log also records the addresses and ports of the connections your devices open.
  - Logs never contain private keys.
  - A log leaves the device only if you export or share it yourself.

Deleting Open Bars removes this data. One exception: on Apple devices, the Connect private key stays in the Keychain so that pairing survives a reinstall. On Windows, uninstalling removes everything, unless you run `OpenBars.exe uninstall --keep-data` to keep the pairing.

## What goes over the network

- **Between your devices, on your hotspot:**
  - Pairing sends the connecting device's name, device type and public key to the iPhone, which answers with the tunnel settings.
  - After that, the device's traffic travels to the iPhone through an encrypted tunnel, using the WireGuard protocol.
  - If you turn on the config page in Settings, anyone on your hotspot can open it. It offers a device's config (for the WireGuard app) once, within 15 minutes of adding the device or tapping Allow Download on it.
  - A connected device can open a status page on the iPhone (`http://10.66.66.1`) through its tunnel. It shows that device its own usage and limits, never other devices'. If it asks for more time or data, the request and any message go to the iPhone.
- **From the iPhone to the internet:**
  - Open Bars re-opens each connection your devices make and sends it out over the iPhone's cellular connection, or through the iPhone's VPN if one is on.
  - Websites and services see the iPhone's address, and your carrier carries the traffic like the iPhone's own.
  - Open Bars passes the traffic along without reading, storing or changing its contents. It counts only how many bytes each device sends and receives.
- **DNS:**
  - Your devices' DNS lookups go to the DNS servers set in Open Bars (Settings › Tunnel). By default these are Cloudflare (1.1.1.1) and Google (8.8.8.8), whose own privacy policies apply.
  - You can replace them with any servers you choose.
- **Diagnose on Windows** checks the internet connection by fetching `http://www.msftconnecttest.com/connecttest.txt`, the same check Windows itself uses. That request is made only when you run Diagnose.

Notifications are posted by Open Bars on the iPhone itself; they don't go through any server.

Open Bars makes no other network requests. The Source Code and Privacy links open in your browser only when you tap them.

## Apple and Microsoft

If you install Open Bars from the App Store or TestFlight, Apple may share crash reports and usage statistics with the developer if you've allowed sharing with app developers, along with any feedback you send through TestFlight. Apple's handling of that data is covered by [Apple's privacy policy](https://www.apple.com/legal/privacy/). Windows' own services are covered by Microsoft's privacy statement.

## Changes

Changes to this policy are made in this file. Its full history is in the repository.

## Contact

Questions about privacy: [open an issue](https://github.com/aayy-dev/OpenBars/issues) on GitHub.
