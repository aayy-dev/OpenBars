//go:build darwin

package engine

import (
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// bindSocket pins a socket to one interface before it binds or connects
// (IP_BOUND_IF / IPV6_BOUND_IF), so its traffic leaves through that
// interface whatever the routing table says. ifIndex <= 0 leaves it alone.
// network is "tcp4", "tcp6", "udp4" or "udp6".
func bindSocket(network string, c syscall.RawConn, ifIndex int) error {
	if ifIndex <= 0 {
		return nil
	}
	var serr error
	err := c.Control(func(fd uintptr) {
		if strings.HasSuffix(network, "6") {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, ifIndex)
		} else {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

// sysInfo describes the OS and device for the session log, e.g.
// "iOS 17.3.1, iPhone15,2".
func sysInfo() string {
	ver, _ := unix.Sysctl("kern.osproductversion")
	name, model := "macOS", "hw.model"
	if runtime.GOOS == "ios" {
		name, model = "iOS", "hw.machine"
	}
	m, _ := unix.Sysctl(model)
	s := name
	if ver != "" {
		s += " " + ver
	}
	if m != "" {
		s += ", " + m
	}
	return s
}

// listenICMP opens an unprivileged ICMP (or ICMPv6) datagram socket, pinned
// to ifIndex before first use when ifIndex > 0.
func listenICMP(v6 bool, ifIndex int) (net.PacketConn, error) {
	family, proto := unix.AF_INET, unix.IPPROTO_ICMP
	if v6 {
		family, proto = unix.AF_INET6, unix.IPPROTO_ICMPV6
	}
	fd, err := unix.Socket(family, unix.SOCK_DGRAM, proto)
	if err != nil {
		return nil, err
	}
	if ifIndex > 0 {
		if v6 {
			err = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, ifIndex)
		} else {
			err = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex)
		}
		if err != nil {
			unix.Close(fd)
			return nil, err
		}
	}
	if !v6 {
		// Without this Darwin hands back IPv4 ICMP with the IP header in front.
		if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_STRIPHDR, 1); err != nil {
			unix.Close(fd)
			return nil, err
		}
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "icmp")
	defer f.Close() // FilePacketConn dups the descriptor
	return net.FilePacketConn(f)
}
