# App Store Connect text

Drafts for the App Store listing, TestFlight and App Review, so each field says the same thing as the README and [PRIVACY.md](../PRIVACY.md). Character limits are App Store Connect's.

## App information

| Field | Value |
| --- | --- |
| Name | Open Bars: Hotspot Sharing |
| Subtitle (30) | Encrypted hotspot sharing |
| Category | Utilities |
| Privacy Policy URL | https://github.com/aayy-dev/OpenBars/blob/main/PRIVACY.md |
| Support URL | https://github.com/aayy-dev/OpenBars/issues |
| Marketing URL | https://github.com/aayy-dev/OpenBars |
| Copyright | 2026 aayy-dev |
| Age rating | Answer "None" or "No" to everything (4+). Open Bars isn't a web browser, so "Unrestricted Web Access" is No. |
| App Privacy | **Data Not Collected** |
| Encryption | Already answered by `ITSAppUsesNonExemptEncryption = false` in the Info.plist. |
| Sign-in required | Off (there are no accounts) |

## App Store listing

**Promotional text (170):**

> Pair your Mac, iPad or PC once. From then on, its tunnel turns on by itself whenever it joins your iPhone's hotspot.

**Description (4000):**

> Open Bars shares your iPhone's cellular connection with your other devices through an encrypted tunnel.
>
> Turn on Personal Hotspot and tap Start. A Mac, iPad, another iPhone or a Windows PC joins the hotspot and pairs once: you just tap Allow. After that, its tunnel turns on by itself whenever it's on your hotspot and stays off everywhere else. On the iPhone, Open Bars opens each connection itself, so your devices' traffic goes out as the iPhone's own.
>
> • Pair once. No accounts, certificates or profiles to install.
> • Automatic. Connect's VPN turns on only on that iPhone's hotspot (on a Mac, over Wi‑Fi or a USB cable).
> • Keep sharing in the background, or start and stop from Control Center, a widget, Siri or Shortcuts.
> • See live speed and each device's data use for today, the last 7 days and the last 30 days.
> • Diagnose checks each step and says what to fix when something doesn't work.
> • Other devices can connect with the WireGuard app, using a QR code or config file.
> • Open source. Open Bars collects no data.
>
> Your carrier still carries this traffic as cellular data from your iPhone. Check your plan's terms for how it treats hotspot use.
>
> Control Center controls and widgets need iOS 18 or later. Open Bars for Windows is available from the Open Bars website.
>
> “WireGuard” and the “WireGuard” logo are registered trademarks of Jason A. Donenfeld.

**Keywords (100):**

```
hotspot,tethering,tunnel,vpn,cellular,share,personal hotspot,laptop,pc,mac,ipad,data usage
```

Don't add "WireGuard" or other apps' names: guideline 2.3.7 bars packing metadata with trademarked terms. The listing also leaves out prices (2.3.7) and other mobile platforms (2.3.10), which is why the app and the listing say "other phones" rather than naming them.

**What's New:** leave empty for the first version.

## TestFlight

**Beta App Description (4000):**

> Open Bars shares your iPhone's cellular connection with your Mac, iPad, another iPhone or a Windows PC through an encrypted tunnel. Pair a device once and its tunnel turns on by itself whenever it joins your hotspot. Open Bars collects no data.

**What to Test:**

> Share from your iPhone to every kind of device you have: Mac, iPad, iPhone, Windows PC (download from github.com/aayy-dev/OpenBars/releases), or anything with the WireGuard app. Try:
> • Pairing, then leaving and rejoining the hotspot: the tunnel should turn off and back on by itself.
> • Settings › Keep sharing in the background, then locking the iPhone.
> • Control Center, the widget and the Shortcuts actions (iOS 18).
> If something doesn't work, run Diagnose on the connecting device and send feedback with a screenshot, or open an issue on GitHub with the log.

**Feedback email and Beta App Review contact:** yours (not stored here).

## App Review notes

Use these for both TestFlight external testing and App Store review (Notes, 4000):

> Open Bars needs two devices to see it working: an iPhone with a cellular plan that includes Personal Hotspot (it shares), and a second device that joins that hotspot (an iPad, iPhone or Mac that runs Open Bars). There are no accounts or sign-in.
>
> 1. Sharing iPhone: turn on Settings › Personal Hotspot › Allow Others to Join. Open Open Bars › Share and tap Start. Allow Local Network access.
> 2. Second device: join that hotspot, open Open Bars › Connect, tap Pair with iPhone, then tap Allow on the sharing iPhone. Tap Add VPN and allow the VPN configuration.
> 3. The second device now browses through the tunnel, and Share shows its live speed and data use.
>
> Without a second Open Bars device: in Share, tap Add › With the WireGuard app. It creates a QR code and config file for the WireGuard app on any device.
>
> How Open Bars uses the Network Extension (NETunnelProviderManager):
> • Connect (iPhone, iPad, Mac): a packet tunnel that sends that device's traffic to the sharing iPhone. On-demand rules turn it on only on that iPhone's hotspot.
> • Keep sharing in the background (optional, off by default, Share › Settings): the sharing server runs in a packet tunnel provider so it keeps running when the iPhone is locked. That tunnel sets no routes and carries none of the iPhone's own traffic.
>
> Open Bars collects no data and has no servers. The Share and Connect screens say so before first use, with a link to the privacy policy (guideline 5.4). The developer is enrolled as an organization. The app is open source: https://github.com/aayy-dev/OpenBars
>
> A screen recording of the two-device setup is attached.

Attach a screen recording of the steps above (both devices, start to browsing). The reviewer may not have a hotspot plan or a second device, so a recording helps the review go through.

**Mac version:** the same notes. The Mac app only connects (a Mac can't share Personal Hotspot), so the reviewer needs an iPhone running Open Bars to share.

## Screenshots

App Store Connect asks for screenshots for each device family the app supports: iPhone (6.9", 1320×2868), iPad (13", 2064×2752) and Mac (2880×1800). [`ios-app/tools/screenshots/make.sh`](../ios-app/tools/screenshots/make.sh) makes all of them:

- It runs Debug builds in demo mode (`-demo`, see `Client/DemoMode.swift`): sample devices, live speed and a month of usage, and nothing is saved. Demo mode isn't in Release builds.
- It captures each screen in the simulators, and the Mac app captures its own window.
- It adds a caption above each one, in a device frame on a green background.

| # | Screen | Caption |
| --- | --- | --- |
| 1 | Share, sharing with three devices | Share your iPhone's data |
| 2 | Usage, last 7 days (iPhone) | See what each device uses |
| 3 | Connect, connected | Connects by itself |
| 4 | Add Device | Set up any device |
| 5 | First launch | Simple from the first tap |
| 6 | Get Started steps (iPhone) | No accounts. No data collected. |
| Mac | Connect window, light and dark | Your Mac, online anywhere · Live speed, at a glance |
