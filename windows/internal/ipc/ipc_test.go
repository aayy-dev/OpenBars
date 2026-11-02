package ipc

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestCallRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ob.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets:", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, l, func(_ context.Context, req Request) (any, string, error) {
		switch req.Op {
		case OpLogs:
			return map[string]int64{"next": req.After + 1}, "", nil
		case OpPair:
			return nil, "Tap Allow on the iPhone.", errors.New("nobody tapped Allow")
		}
		return nil, "", errors.New("unknown op")
	})
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()

	var out struct{ Next int64 }
	if err := Call(cctx, dial, Request{Op: OpLogs, After: 41}, &out); err != nil || out.Next != 42 {
		t.Fatalf("logs: %+v %v", out, err)
	}
	err = Call(cctx, dial, Request{Op: OpPair}, nil)
	var se *ServiceError
	if !errors.As(err, &se) || se.Hint != "Tap Allow on the iPhone." {
		t.Fatalf("pair error: %v", err)
	}
	cancel()
	noDial := func(context.Context) (net.Conn, error) { return nil, errors.New("pipe not found") }
	if err := Call(context.Background(), noDial, Request{Op: OpStatus}, nil); !errors.Is(err, ErrNoService) {
		t.Fatalf("no service: %v", err)
	}
}
