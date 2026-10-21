package rpc

import (
	"fmt"
	"net"
	"os"
)

// NotifySocketEnv is systemd's notification socket variable.
const NotifySocketEnv = "NOTIFY_SOCKET"

// NotifyReady tells systemd that start-up reconcile finished and the
// socket accepts calls (KTD10, Type=notify). It does nothing when
// NOTIFY_SOCKET is not set. A name that starts with '@' is an abstract
// socket.
func NotifyReady() error {
	return notify(os.Getenv(NotifySocketEnv), "READY=1")
}

// NotifyStopping tells systemd that the server is stopping.
func NotifyStopping() error {
	return notify(os.Getenv(NotifySocketEnv), "STOPPING=1")
}

func notify(addr, state string) error {
	if addr == "" {
		return nil
	}
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("rpc: dial the notify socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("rpc: notify systemd: %w", err)
	}
	return nil
}
