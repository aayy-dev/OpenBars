package engine

import "github.com/aayy-dev/OpenBars/ios-app/core/internal/applog"

// Logger is the engine's leveled log (internal/applog): entries with a
// level, a source ("share", "wg", "tcp", "udp", "dns", "icmp", "egress",
// "nat64", "pair", "web", "usage", "diag") and an optional hint.
type Logger = applog.Logger

// NewLog returns a log that keeps the last n entries.
func NewLog(n int) *Logger { return applog.New(n) }

// shortKey shortens a base64 public key for logs. Never pass a private key.
func shortKey(pub string) string { return applog.ShortKey(pub) }
