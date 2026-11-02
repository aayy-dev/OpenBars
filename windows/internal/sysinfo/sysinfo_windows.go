// Package sysinfo describes this PC for logs and pairing.
package sysinfo

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ComputerName is the PC's name as shown in Settings › System › About
// (what the iPhone lists it as).
func ComputerName() string {
	n := uint32(256)
	buf := make([]uint16, n)
	if windows.GetComputerNameEx(windows.ComputerNameDnsHostname, &buf[0], &n) == nil && n > 0 {
		return windows.UTF16ToString(buf[:n])
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "Windows PC"
}

// OS is e.g. "Windows 11 Pro 25H2 (26200.7019)".
func OS() string {
	v := windows.RtlGetVersion()
	name := "Windows 10"
	if v.MajorVersion == 10 && v.BuildNumber >= 22000 {
		name = "Windows 11" // the registry's ProductName still says 10
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return fmt.Sprintf("%s (%d)", name, v.BuildNumber)
	}
	defer k.Close()
	if ed, _, err := k.GetStringValue("EditionID"); err == nil {
		switch ed {
		case "Professional":
			name += " Pro"
		case "Core":
			name += " Home"
		case "Enterprise", "Education":
			name += " " + ed
		}
	}
	if dv, _, err := k.GetStringValue("DisplayVersion"); err == nil && dv != "" {
		name += " " + dv
	}
	build := fmt.Sprint(v.BuildNumber)
	if ubr, _, err := k.GetIntegerValue("UBR"); err == nil {
		build += fmt.Sprintf(".%d", ubr)
	}
	return name + " (" + build + ")"
}

// Model is the PC's maker and model, e.g. "Gigabyte B450 AORUS PRO WIFI".
func Model() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	maker, _, _ := k.GetStringValue("SystemManufacturer")
	model, _, _ := k.GetStringValue("SystemProductName")
	maker, model = strings.TrimSpace(maker), strings.TrimSpace(model)
	if strings.Contains(strings.ToLower(maker), "to be filled") || maker == "System manufacturer" {
		maker = ""
	}
	return strings.TrimSpace(maker + " " + model)
}
