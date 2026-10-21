package rpc

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// readPeer reads LOCAL_PEERCRED and LOCAL_PEERPID from a Unix socket
// connection.
func readPeer(conn net.Conn) (PeerInfo, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return PeerInfo{}, errors.New("not a Unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return PeerInfo{}, err
	}
	var info PeerInfo
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credErr != nil {
			return
		}
		info.UID = int(cred.Uid)
		if pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			info.PID = pid
		}
	}); err != nil {
		return PeerInfo{}, err
	}
	return info, credErr
}

// noProc is the inspector of a platform with no /proc. macOS can read
// another process's cwd only through libproc, which needs cgo, so every
// cwd check is recorded as unverified there.
type noProc struct{}

// DefaultProc is the platform's ProcInspector.
var DefaultProc ProcInspector = noProc{}

func (noProc) Cwd(int) (string, error) { return "", ErrProcUnsupported }
func (noProc) Parent(int) (int, error) { return 0, ErrProcUnsupported }
