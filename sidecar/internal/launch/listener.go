// Package launch owns the one-way process readiness handshake.
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
)

const ReadyContract = "vibetable.sidecar.ready.v1"

type Ready struct {
	Contract string         `json:"contract"`
	Event    string         `json:"event"`
	Address  string         `json:"address"`
	PID      int            `json:"pid"`
	Build    buildinfo.Info `json:"build"`
}

func OpenLoopback() (net.Listener, error) {
	return openLoopback("127.0.0.1:0")
}

func openLoopback(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, fmt.Errorf("bind loopback listener: %w", err)
	}
	return listener, nil
}

type announcingListener struct {
	net.Listener

	announce func(address string) error
	once     sync.Once
	err      error
}

// AnnounceOnFirstAccept wraps an already-bound listener and invokes announce
// immediately before the HTTP server performs its first blocking Accept.
// This is late enough to prove PocketBase successfully built its handler and
// entered Server.Serve, while still emitting readiness before the accept loop
// can block waiting for a client.
func AnnounceOnFirstAccept(
	listener net.Listener,
	announce func(address string) error,
) (net.Listener, error) {
	if listener == nil {
		return nil, errors.New("listener is required")
	}
	if announce == nil {
		return nil, errors.New("ready announcer is required")
	}
	return &announcingListener{
		Listener: listener,
		announce: announce,
	}, nil
}

func (listener *announcingListener) Accept() (net.Conn, error) {
	listener.once.Do(func() {
		listener.err = listener.announce(listener.Addr().String())
	})
	if listener.err != nil {
		_ = listener.Listener.Close()
		return nil, fmt.Errorf("announce listener readiness: %w", listener.err)
	}
	return listener.Listener.Accept()
}

func ReadyRecord(address string, build buildinfo.Info) Ready {
	return Ready{
		Contract: ReadyContract,
		Event:    "sidecar.ready",
		Address:  address,
		PID:      os.Getpid(),
		Build:    build,
	}
}

func WriteReady(writer io.Writer, ready Ready) error {
	if writer == nil {
		return errors.New("ready writer is required")
	}
	host, port, err := net.SplitHostPort(ready.Address)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host != "127.0.0.1" || portNumber < 1 || portNumber > 65_535 {
		return errors.New("ready address must be an assigned IPv4 loopback port")
	}
	if err := json.NewEncoder(writer).Encode(ready); err != nil {
		return fmt.Errorf("write ready handshake: %w", err)
	}
	return nil
}
