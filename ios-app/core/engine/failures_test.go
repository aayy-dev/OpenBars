package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFailureSummaries(t *testing.T) {
	log := NewLog(50)
	var f failTally
	for i := 0; i < 9; i++ {
		f.add(log, "tcp", "connection refused", fmt.Sprintf("1.2.3.%d:443", i), nil)
	}
	for i := 0; i < 5; i++ {
		f.add(log, "tcp", "timed out", "142.250.72.14:443", nil)
	}
	f.add(log, "dns", "timed out", "1.1.1.1:53", nil)
	if log.Tail() != "" {
		t.Fatal("failures must not be logged one by one")
	}
	now := time.Now().Add(61 * time.Second)
	f.flush(log, now, false)
	tail := log.Tail()
	want := "warn tcp TCP: 14 connections failed in the last minute (connection refused ×9, timed out ×5); last: 142.250.72.14:443 ↳ Check that cellular data is on for Open Bars."
	if !strings.Contains(tail, want) || !strings.Contains(tail, "warn dns DNS: 1 upstream query failed in the last minute (timed out ×1); last: 1.1.1.1:53 ↳ Check the upstream DNS servers") {
		t.Fatalf("got:\n%s\nwant line:\n%s", tail, want)
	}
	// Within a minute of the last summary: held back, then logged.
	f.add(log, "tcp", "connection refused", "5.6.7.8:80", nil)
	f.flush(log, now.Add(30*time.Second), false)
	if strings.Count(log.Tail(), "warn tcp") != 1 {
		t.Fatal("summarized again within a minute")
	}
	f.flush(log, now.Add(61*time.Second), false)
	if !strings.Contains(log.Tail(), "TCP: 1 connection failed in the last") {
		t.Fatal(log.Tail())
	}
	// Detailed logging adds a debug line per failure.
	log.SetDetailed(true)
	f.add(log, "udp", "network unreachable", "9.9.9.9:443", syscall.ENETUNREACH)
	if !strings.Contains(log.Tail(), "debug udp UDP to 9.9.9.9:443 failed") {
		t.Fatal(log.Tail())
	}
	f.flush(log, now.Add(200*time.Second), true)
	if !strings.Contains(log.Tail(), "UDP: 1 datagram couldn't be sent") {
		t.Fatal(log.Tail())
	}
}

func TestFailReason(t *testing.T) {
	for err, want := range map[error]string{
		syscall.ECONNREFUSED:                        "connection refused",
		fmt.Errorf("dial: %w", syscall.ENETUNREACH): "network unreachable",
		syscall.EHOSTUNREACH:                        "host unreachable",
		context.DeadlineExceeded:                    "timed out",
		fmt.Errorf("x: %w", errNoNAT64):             reasonNoNAT64,
		errors.New("read udp: something odd"):       "something odd",
		fmt.Errorf("op: %w", syscall.EPERM):         "not permitted",
		fmt.Errorf("op: %w", syscall.EADDRNOTAVAIL): "address not available",
	} {
		if got := failReason(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
	if period(5*time.Second) != "in the last 5 s" || period(60*time.Second) != "in the last minute" || period(5*time.Minute) != "in the last 5 min" {
		t.Fatal("period")
	}
}
