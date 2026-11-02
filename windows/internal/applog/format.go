package applog

import (
	"fmt"
	"strings"
	"time"
)

// Display format shared by every Open Bars app:
//
//	2026-11-02 13:04:05.123  WARN   tcp     message (×3 more)
//	                                        ↳ hint
const stampLayout = "2006-01-02 15:04:05.000"

// hintIndent lines a hint up under the message column.
var hintIndent = strings.Repeat(" ", len(stampLayout)+2+6+1+6+2)

// Stamp formats an entry's time in loc (the device's zone by default).
func Stamp(e Entry, loc *time.Location) string {
	return e.Time().In(loc).Format(stampLayout)
}

// LevelLabel is the padded, upper-case level ("INFO ", "ERROR").
func LevelLabel(l Level) string {
	return fmt.Sprintf("%-5s", strings.ToUpper(string(l)))
}

// Message is the entry's text as shown, including a repeat summary.
func Message(e Entry) string {
	if e.Rep > 0 {
		return fmt.Sprintf("%s (×%d more)", e.Msg, e.Rep)
	}
	return e.Msg
}

// Line formats one entry; a hint goes on its own indented line.
func Line(e Entry, loc *time.Location) string {
	s := fmt.Sprintf("%s  %s  %-6s  %s", Stamp(e, loc), LevelLabel(e.Level), e.Src, Message(e))
	if e.Hint != "" {
		s += "\n" + hintIndent + "↳ " + e.Hint
	}
	return s
}

// ZoneLabel describes loc for the export header, e.g. "local time (CST, UTC−06:00)".
func ZoneLabel(loc *time.Location, at time.Time) string {
	name, off := at.In(loc).Zone()
	sign := "+"
	if off < 0 {
		sign, off = "−", -off
	}
	utc := fmt.Sprintf("UTC%s%02d:%02d", sign, off/3600, off%3600/60)
	if loc == time.UTC {
		return "UTC"
	}
	if name == "" || strings.HasPrefix(name, "+") || strings.HasPrefix(name, "-") {
		return "local time (" + utc + ")"
	}
	return "local time (" + name + ", " + utc + ")"
}

// Header describes where a log came from, for the top of an export.
type Header struct {
	App      string // "Open Bars 1.0.0 (3)"
	OS       string // "Windows 11 Pro 24H2 (26100.1234)"
	Device   string // computer model or name
	Which    string // "Connect"
	Location *time.Location
	Now      time.Time
}

// Export renders a header and every entry, oldest first.
func Export(h Header, entries []Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s log\n", h.App, h.Which)
	if h.OS != "" {
		fmt.Fprintf(&b, "System: %s\n", h.OS)
	}
	if h.Device != "" {
		fmt.Fprintf(&b, "Device: %s\n", h.Device)
	}
	fmt.Fprintf(&b, "Times: %s\n", ZoneLabel(h.Location, h.Now))
	fmt.Fprintf(&b, "Exported: %s\n", h.Now.In(h.Location).Format(stampLayout))
	fmt.Fprintf(&b, "Entries: %d\n\n", len(entries))
	for _, e := range entries {
		b.WriteString(Line(e, h.Location))
		b.WriteByte('\n')
	}
	return b.String()
}
