package ipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// PipeName is the service's named pipe.
const PipeName = `\\.\pipe\OpenBarsConnect`

// Listen opens the pipe. Only LocalSystem, Administrators and the
// interactively signed-in user can connect.
func Listen() (net.Listener, error) {
	return winio.ListenPipe(PipeName, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)",
		MessageMode:        false,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   256 << 10,
	})
}

// Dial connects to the service's pipe.
func Dial(ctx context.Context) (net.Conn, error) {
	return winio.DialPipeContext(ctx, PipeName)
}
