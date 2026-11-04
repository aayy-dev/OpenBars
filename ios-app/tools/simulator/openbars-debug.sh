#!/bin/bash
# Runs an Open Bars debug command in a Simulator without taps (Debug builds
# only; see Store.handleDebugURL in Sources/Store+Limits.swift):
#
#   tools/simulator/openbars-debug.sh approve-ask
#   tools/simulator/openbars-debug.sh "allow?limit=like"
#   tools/simulator/openbars-debug.sh "give?what=15m&device=0"
#
# SIM picks the Simulator (default: the booted one). Pair with
# core/cmd/fakedevice, which plays a connected device from this Mac.
set -euo pipefail
SIM=${SIM:-booted}
[ $# -eq 1 ] || { echo "usage: $0 <command>[?args]" >&2; exit 2; }
DATA=$(xcrun simctl get_app_container "$SIM" dev.aayy.openbars data)
mkdir -p "$DATA/Library/Application Support"
echo "openbars://debug/$1" > "$DATA/Library/Application Support/debug-command.txt"
xcrun simctl spawn "$SIM" notifyutil -p dev.aayy.openbars.debug
