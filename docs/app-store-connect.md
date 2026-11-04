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
> Turn on Personal Hotspot and tap Start. A Mac, iPad, another iPhone or a Windows PC joins the hotspot and pairs once: you just tap Allow. After that, its tunnel turns on by itself whenever it's on your hotspot and stays off everywhere else.
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
> Control Center controls and widgets need iOS 18 or later. Open Bars for Windows is on GitHub: github.com/aayy-dev/OpenBars
>
> “WireGuard” and the “WireGuard” logo are registered trademarks of Jason A. Donenfeld.

**Keywords (100):**

```
tethering,tunnel,vpn,cellular,personal,laptop,mac,ipad,pc,mobile data,usage,internet,usb,connection
```

Apple's search guide says "Don't repeat any words included in your app name, subtitle, or category", so "hotspot", "sharing" and "encrypted" (already in the name and subtitle) aren't here.

Don't add "WireGuard" or other apps' names: guideline 2.3.7 bars packing metadata with trademarked terms. The listing also leaves out prices (2.3.7) and other mobile platforms (2.3.10), which is why the app and the listing say "other phones" rather than naming them.

**What's New:** leave empty for the first version.

### Mac listing

The Mac app only connects, so its listing describes that and leaves out the iPhone's sharing features.

**Promotional text (170):**

> Pair your Mac with your iPhone once. From then on, its tunnel turns on by itself whenever it joins the iPhone's hotspot.

**Description (4000):**

> Open Bars gets your Mac online through your iPhone's cellular connection, over an encrypted tunnel.
>
> On your iPhone, turn on Personal Hotspot and tap Start in Open Bars. Then join the hotspot from your Mac, over Wi‑Fi or a USB cable, and pair once: you just tap Allow on the iPhone. After that, the Mac's tunnel turns on by itself whenever it's on that iPhone's hotspot, and stays off on every other network.
>
> • Pair once. No accounts, certificates or profiles to install.
> • Automatic. The VPN turns on only on that iPhone's hotspot, over Wi‑Fi or a USB cable.
> • Lives in the menu bar: see the connection and its live speed, and connect or disconnect. It can open at login.
> • Diagnose checks each step and says what to fix when something doesn't work.
> • Open source. Open Bars collects no data.
>
> Open Bars on the Mac connects to an iPhone running Open Bars, which shares its connection. Your carrier still carries this traffic as cellular data from your iPhone. Check your plan's terms for how it treats hotspot use.

**Keywords (100):**

```
tethering,tunnel,vpn,cellular,personal,usb,menu bar,laptop,mobile data,internet,connection,network
```

### Availability

Every territory except China mainland. Guideline 5.4: a VPN app offered "in a territory that requires a VPN license" must give its license information in the App Review notes, and China mainland requires one.

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

Use these for TestFlight external testing and App Store review (Notes, 4000). Don't mention a screen recording unless one is attached.

**iPhone and iPad:**

> Open Bars needs two devices to see it working: an iPhone with a cellular plan that includes Personal Hotspot (it shares), and a second device that joins that hotspot (an iPad, iPhone or Mac running Open Bars). There are no accounts or sign-in.
>
> 1. Sharing iPhone: turn on Settings › Personal Hotspot › Allow Others to Join. Open Open Bars, choose "Share this iPhone's data" (or the Share tab) and tap Start. Allow Local Network access.
> 2. Second device: join that hotspot, open Open Bars › Connect, tap Pair with iPhone, then tap Allow on the sharing iPhone. Tap Add VPN and allow the VPN configuration.
> 3. The second device now browses through the tunnel, and Share shows its live speed and data use.
>
> Without a second Open Bars device: in Share, tap Add › Something else (With the WireGuard app). It creates a QR code and config file for the WireGuard app on any device.
>
> How Open Bars uses the Network Extension (NETunnelProviderManager):
> • Connect (iPhone, iPad, Mac): a packet tunnel that sends that device's traffic to the sharing iPhone. On-demand rules turn it on only on that iPhone's hotspot.
> • Keep sharing in the background (optional, off by default, Share › Settings): the sharing server runs in a packet tunnel provider so it keeps running when the iPhone is locked. That tunnel sets no routes and carries none of the iPhone's own traffic.
>
> Open Bars collects no data and has no servers. The first screen and the Share and Connect setup say so before use, with a link to the privacy policy (guideline 5.4). The app isn't offered in China mainland. The developer is enrolled as an organization. The app is open source: https://github.com/aayy-dev/OpenBars

**Mac:**

> The Mac app connects a Mac to an iPhone that shares its cellular connection with Open Bars (the iOS app in this same App Store record). A Mac can't share Personal Hotspot, so reviewing the Mac app needs an iPhone with a cellular plan that includes Personal Hotspot, running Open Bars. There are no accounts or sign-in.
>
> 1. iPhone: turn on Settings › Personal Hotspot › Allow Others to Join. Open Open Bars › Share and tap Start. Allow Local Network access.
> 2. Mac: join the iPhone's hotspot over Wi‑Fi (or connect a USB cable). Open Open Bars, click Pair with iPhone, then tap Allow on the iPhone. Click Add VPN and allow the VPN configuration.
> 3. The Mac now browses through the tunnel. The window and the menu bar show the connection and its live speed. On any other network the VPN stays off.
>
> How Open Bars uses the Network Extension: a packet tunnel (NETunnelProviderManager) that sends the Mac's traffic to the sharing iPhone. On-demand rules turn it on only on that iPhone's hotspot.
>
> Open Bars collects no data and has no servers. The app says so before first use, with a link to the privacy policy (guideline 5.4). The app isn't offered in China mainland. The developer is enrolled as an organization. The app is open source: https://github.com/aayy-dev/OpenBars

## Screenshots

App Store Connect requires iPhone screenshots for the 6.3" Dynamic Island display (1206×2622), plus iPad 13" (2064×2752) and Mac (2880×1800). iPhone Duo (2853×2007 open, or 1398×2034 closed) is optional until April 2027 for apps built with the iOS 27.1 SDK, and earns the "optimized for iPhone Duo" badge. [`ios-app/tools/screenshots/make.sh`](../ios-app/tools/screenshots/make.sh) captures the screens; `frame.swift` frames them at any of these sizes:

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
| iPad | Connect (light, dark) and first launch, each with the iPhone it connects through (`frame.swift` with a second screenshot) | Get online through your iPhone · Live speed, at a glance · Set up in a minute |
| Mac | Connect window, light and dark | Your Mac, online anywhere · Live speed, at a glance |

**Header and search results** (the App Store's product page header and search result art): [`creative.swift`](../ios-app/tools/screenshots/creative.swift) makes the header (3840×1646) and search asset (3840×2560) from the raw captures, with the iPhone sharing to a connected Mac on a dark backdrop. On an iPhone, App Store Connect's preview shows about the middle 83% of the header's width, and the search asset's full width from its top down to about y 1850. Keep the devices large, and the headline's top margin equal to the side margins.
