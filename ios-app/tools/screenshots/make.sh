#!/bin/sh
# App Store screenshots: runs Debug builds in demo mode (sample devices and
# traffic, see Client/DemoMode.swift), captures each screen, and adds the
# captions. Run from anywhere; takes a few minutes.
#
#   ios-app/tools/screenshots/make.sh [out dir]      (default /tmp/openbars-screenshots)
#
# Simulators (names or UDIDs): IPHONE="iPhone 17 Pro Max" (6.9", 1320x2868),
# IPAD="iPad Pro 13-inch (M5)" (13", 2064x2752). The Mac app captures its own
# window (2x), so no screen-recording permission is needed.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
project="$here/../../OpenBars"
out=${1:-/tmp/openbars-screenshots}
raw="$out/raw"
IPHONE=${IPHONE:-"iPhone 17 Pro Max"}
IPAD=${IPAD:-"iPad Pro 13-inch (M5)"}
dd=/tmp/openbars-screenshots-build
mkdir -p "$raw" "$out/iphone-6.9" "$out/ipad-13" "$out/mac"

swiftc -O "$here/frame.swift" -o "$dd-frame"
(cd "$project" && xcodegen generate >/dev/null)
for scheme in "OpenBars:generic/platform=iOS Simulator" "OpenBarsMac:generic/platform=macOS"; do
  xcodebuild -project "$project/OpenBars.xcodeproj" -scheme "${scheme%%:*}" -configuration Debug \
    -destination "${scheme#*:}" -derivedDataPath "$dd" CODE_SIGNING_ALLOWED=NO build >/dev/null
done
app="$dd/Build/Products/Debug-iphonesimulator/OpenBars.app"
macapp="$dd/Build/Products/Debug/Open Bars.app/Contents/MacOS/Open Bars"

# shot <device> <file> [launch arguments]: a fresh launch, then a capture
# once it has settled (slow simulators need the wait).
shot() {
  dev=$1; file=$2; shift 2
  xcrun simctl terminate "$dev" dev.aayy.openbars >/dev/null 2>&1 || true
  xcrun simctl launch "$dev" dev.aayy.openbars "$@" >/dev/null
  sleep 15
  xcrun simctl io "$dev" screenshot "$raw/$file" >/dev/null 2>&1
  echo "$raw/$file"
}

# prepare <device>: booted, 9:41 with full bars, light mode, a fresh install.
prepare() {
  xcrun simctl boot "$1" 2>/dev/null || true
  xcrun simctl bootstatus "$1" -b >/dev/null
  xcrun simctl status_bar "$1" override --time "9:41" --dataNetwork 5g --wifiMode active --wifiBars 3 \
    --cellularMode active --cellularBars 4 --batteryState discharging --batteryLevel 100
  xcrun simctl ui "$1" appearance light
  xcrun simctl uninstall "$1" dev.aayy.openbars 2>/dev/null || true
  xcrun simctl install "$1" "$app"
}

demo="-demo -welcomeDone YES"
for kind in iphone ipad; do
  dev=$([ $kind = iphone ] && echo "$IPHONE" || echo "$IPAD")
  prepare "$dev"
  shot "$dev" $kind-1-share.png $demo -tab share
  [ $kind = iphone ] && shot "$dev" $kind-2-usage.png $demo -tab share -showUsage
  shot "$dev" $kind-3-connect.png $demo -tab connect
  shot "$dev" $kind-4-add.png $demo -tab share -showAddDevice
  shot "$dev" $kind-5-welcome.png                       # first launch
  [ $kind = iphone ] && shot "$dev" $kind-6-getstarted.png -welcomeDone YES -tab share
  xcrun simctl shutdown "$dev"
done
"$macapp" -demo -appearance light -ApplePersistenceIgnoreState YES -snapshot "$raw/mac-1-connect.png" 2>/dev/null
"$macapp" -demo -appearance dark -ApplePersistenceIgnoreState YES -snapshot "$raw/mac-2-connect-dark.png" 2>/dev/null

caption() {
  case "$1" in
    *mac-1*)      echo "Your Mac, online anywhere|It connects through your iPhone by itself, over Wi‑Fi or a USB cable, and stays off on other networks." ;;
    *mac-2*)      echo "Live speed, at a glance|See the connection and its speed in the window or the menu bar." ;;
    *share*)      echo "Share your iPhone's data|Your Mac, iPad and PC get online through your iPhone, over an encrypted tunnel." ;;
    *usage*)      echo "See what each device uses|Live speed, plus usage for today, the last 7 days and the last 30 days." ;;
    *connect*)    echo "Connects by itself|Pair once. The tunnel turns on whenever you join your iPhone's hotspot, and off everywhere else." ;;
    *add*)        echo "Set up any device|Step-by-step help for iPhone, iPad, Mac and Windows PCs." ;;
    *welcome*)    echo "Simple from the first tap|One question, then each step, ticked off as you go." ;;
    *getstarted*) echo "No accounts. No data collected.|Nothing is sent to the developer, and Open Bars is open source." ;;
  esac
}
for f in "$raw"/*.png; do
  c=$(caption "$f"); name=$(basename "$f")
  case "$name" in
    iphone-*) "$dd-frame" "$f" "$out/iphone-6.9/$name" 1320 2868 phone "${c%%|*}" "${c#*|}" ;;
    ipad-*)   "$dd-frame" "$f" "$out/ipad-13/$name" 2064 2752 tablet "${c%%|*}" "${c#*|}" ;;
    mac-*)    "$dd-frame" "$f" "$out/mac/$name" 2880 1800 mac "${c%%|*}" "${c#*|}" ;;
  esac
done
echo "Done: $out (raw captures in $raw)"
