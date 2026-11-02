//go:build !darwin

package engine

import (
	"net"
	"runtime"
	"syscall"

	"golang.org/x/net/icmp"
)

// bindSocket is a no-op off Apple platforms (Linux CI and tests).
func bindSocket(network string, c syscall.RawConn, ifIndex int) error { return nil }

func sysInfo() string { return runtime.GOOS + "/" + runtime.GOARCH }

func ifaceCounters() (map[string][2]uint64, error) { return nil, nil }

// listenICMP opens an unprivileged ICMP (or ICMPv6) socket; it can't be
// pinned off Apple platforms.
func listenICMP(v6 bool, ifIndex int) (net.PacketConn, error) {
	if v6 {
		return icmp.ListenPacket("udp6", "::")
	}
	return icmp.ListenPacket("udp4", "0.0.0.0")
}
