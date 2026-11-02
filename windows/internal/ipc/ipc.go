// Package ipc connects the tray app (running as the signed-in user) to the
// Open Bars service (running as LocalSystem): one JSON request and one JSON
// response per connection, over a named pipe on Windows.
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Request is a call from the tray app.
type Request struct {
	Op    string `json:"op"`
	After int64  `json:"after,omitempty"`
	On    bool   `json:"on,omitempty"`
}

// Response is the service's answer.
type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Hint  string          `json:"hint,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Operations.
const (
	OpStatus      = "status"
	OpPair        = "pair"
	OpSetAuto     = "set_auto"
	OpConnect     = "connect"
	OpDisconnect  = "disconnect"
	OpForget      = "forget"
	OpDiagnose    = "diagnose"
	OpLogs        = "logs"
	OpClearLogs   = "clear_logs"
	OpSetDetailed = "set_detailed"
)

// Handler answers one request. Returning data with err == nil is success.
type Handler func(ctx context.Context, req Request) (data any, hint string, err error)

// Serve answers requests on l until ctx ends.
func Serve(ctx context.Context, l net.Listener, h Handler) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go serveConn(ctx, c, h)
	}
}

func serveConn(ctx context.Context, c net.Conn, h Handler) {
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var req Request
	if err := json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&req); err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	data, hint, err := h(ctx, req)
	resp := Response{OK: err == nil, Hint: hint}
	if err != nil {
		resp.Error = err.Error()
	} else if data != nil {
		b, merr := json.Marshal(data)
		if merr != nil {
			resp = Response{Error: merr.Error()}
		} else {
			resp.Data = b
		}
	}
	c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	json.NewEncoder(c).Encode(resp)
}

// Dialer opens a connection to the service.
type Dialer func(ctx context.Context) (net.Conn, error)

// ErrNoService means the service isn't running (or isn't installed).
var ErrNoService = errors.New("the Open Bars service isn't running")

// Call sends one request and decodes the response's data into out (if not nil).
// Errors from the service come back as *ServiceError.
func Call(ctx context.Context, dial Dialer, req Request, out any) error {
	c, err := dial(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoService, err)
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return err
	}
	var resp Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return fmt.Errorf("no answer from the Open Bars service: %w", err)
	}
	if !resp.OK {
		return &ServiceError{Msg: resp.Error, Hint: resp.Hint}
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}

// ServiceError is a failure reported by the service, with an optional hint.
type ServiceError struct {
	Msg  string
	Hint string
}

func (e *ServiceError) Error() string { return e.Msg }
