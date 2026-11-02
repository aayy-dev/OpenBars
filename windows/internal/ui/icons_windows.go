package ui

import _ "embed"

// Tray icons, made by tools/mkicons.
var (
	//go:embed icons/tray-connected.ico
	iconConnected []byte
	//go:embed icons/tray-connecting.ico
	iconConnecting []byte
	//go:embed icons/tray-problem.ico
	iconProblem []byte
	//go:embed icons/tray-waiting.ico
	iconWaiting []byte
)
