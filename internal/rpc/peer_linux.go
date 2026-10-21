package rpc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// readPeer reads SO_PEERCRED from a Unix socket connection.
func readPeer(conn net.Conn) (PeerInfo, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return PeerInfo{}, errors.New("not a Unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return PeerInfo{}, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return PeerInfo{}, err
	}
	if credErr != nil {
		return PeerInfo{}, credErr
	}
	return PeerInfo{UID: int(cred.Uid), PID: int(cred.Pid)}, nil
}

// procFS reads /proc.
type procFS struct{}

// DefaultProc is the platform's ProcInspector.
var DefaultProc ProcInspector = procFS{}

func (procFS) Cwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}

func (procFS) Parent(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The command name in field 2 may hold spaces and parentheses, so the
	// fields after it start at the last ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("parse /proc/%d/stat", pid)
	}
	return strconv.Atoi(fields[1])
}
