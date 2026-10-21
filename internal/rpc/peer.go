package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// PeerInfo is what the socket learns about the process at the other end
// (KTD12). PID is 0 when the platform does not report it.
type PeerInfo struct {
	UID int
	PID int
}

// ErrProcUnsupported means the platform gives no way to read another
// process's working directory or parent. The cwd checks then record the
// call as unverified instead of refusing it.
var ErrProcUnsupported = errors.New("rpc: this platform cannot read a process's working directory")

// ProcInspector reads another process's working directory and parent.
type ProcInspector interface {
	Cwd(pid int) (string, error)
	Parent(pid int) (int, error)
}

// peerAuthInfo carries PeerInfo from the handshake to each call.
type peerAuthInfo struct {
	credentials.CommonAuthInfo
	Peer PeerInfo
}

func (peerAuthInfo) AuthType() string { return "loop-peer" }

// CheckPeerUID refuses a peer that runs as another user than the server.
func CheckPeerUID(p PeerInfo, own int) error {
	if p.UID != own {
		return fmt.Errorf("rpc: refuse a peer with uid %d; the server runs as uid %d", p.UID, own)
	}
	return nil
}

// peerCreds is the transport credential of the socket. It exchanges no
// bytes: it reads the peer's uid and pid from the kernel and refuses
// another uid before gRPC sees the connection.
type peerCreds struct {
	ownUID int
	read   func(net.Conn) (PeerInfo, error)
}

func newPeerCreds() credentials.TransportCredentials {
	return &peerCreds{ownUID: os.Getuid(), read: readPeer}
}

func (c *peerCreds) ClientHandshake(_ context.Context, _ string, conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return conn, peerAuthInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.NoSecurity}}, nil
}

func (c *peerCreds) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	p, err := c.read(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("rpc: read peer credentials: %w", err)
	}
	if err := CheckPeerUID(p, c.ownUID); err != nil {
		return nil, nil, err
	}
	return conn, peerAuthInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.NoSecurity}, Peer: p}, nil
}

func (c *peerCreds) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "loop-peer"}
}

func (c *peerCreds) Clone() credentials.TransportCredentials {
	cp := *c
	return &cp
}

func (c *peerCreds) OverrideServerName(string) error { return nil }

// peerFrom returns the PeerInfo of the call in ctx.
func peerFrom(ctx context.Context) (PeerInfo, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return PeerInfo{}, false
	}
	info, ok := p.AuthInfo.(peerAuthInfo)
	if !ok {
		return PeerInfo{}, false
	}
	return info.Peer, true
}

// within reports whether path is dir or sits below it. Both are cleaned,
// and each is compared both as given and with symlinks resolved.
func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	for _, p := range variants(path) {
		for _, d := range variants(dir) {
			rel, err := filepath.Rel(d, p)
			if err == nil && filepath.IsLocal(rel) {
				return true
			}
		}
	}
	return false
}

func variants(p string) []string {
	p = filepath.Clean(p)
	out := []string{p}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
		out = append(out, r)
	}
	return out
}

// ancestry returns the working directories of pid and of each ancestor up
// to pid 1, nearest first. It stops at the first process it cannot read.
func ancestry(pi ProcInspector, pid int) ([]string, error) {
	var cwds []string
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] && len(seen) < 64 {
		seen[pid] = true
		cwd, err := pi.Cwd(pid)
		if err != nil {
			if len(cwds) == 0 {
				return nil, err
			}
			break
		}
		cwds = append(cwds, cwd)
		parent, err := pi.Parent(pid)
		if err != nil {
			break
		}
		pid = parent
	}
	return cwds, nil
}
