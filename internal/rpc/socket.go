package rpc

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Socket errors. Callers test them with errors.Is.
var (
	// ErrInstanceLocked means another server holds the instance's lock.
	ErrInstanceLocked = errors.New("rpc: another server runs on this instance directory")
	// ErrServerRunning means a live server answers on the socket.
	ErrServerRunning = errors.New("rpc: a live server answers on the socket")
)

// LockFileName is the single-instance lock in the instance directory.
const LockFileName = "server.lock"

// InstanceLock is the flock that makes one server per instance. The kernel
// drops it when the process dies, so a SIGKILL leaves no stale lock.
type InstanceLock struct{ f *os.File }

// LockInstance creates the instance directory at mode 0700 when it is
// missing and takes the instance lock without waiting.
func LockInstance(dir string) (*InstanceLock, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("rpc: open the instance lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrInstanceLocked, dir)
		}
		return nil, fmt.Errorf("rpc: lock the instance: %w", err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return &InstanceLock{f: f}, nil
}

// Release drops the lock.
func (l *InstanceLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// ensurePrivateDir creates dir at mode 0700 and refuses a directory that is
// a symlink, belongs to another user or is open to other users.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("rpc: create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("rpc: %s is not a directory", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("rpc: %s belongs to another user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("rpc: %s has mode %v; want 0700", dir, info.Mode().Perm())
	}
	return nil
}

// PrepareSocket makes the socket's private directory and removes a stale
// socket file. It removes the file only after a connect probe finds no live
// server, and it returns ErrServerRunning when one answers.
func PrepareSocket(path string) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("rpc: %s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("%w: %s", ErrServerRunning, path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("rpc: probe %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("rpc: remove the stale socket: %w", err)
	}
	return nil
}

// Bind listens on path with mode 0600. The socket's directory must be
// private (0700, this user), so no other user can reach the socket before
// the chmod. Bind never changes the umask: the umask belongs to the whole
// process, and a process that another goroutine starts meanwhile would
// inherit it.
func Bind(path string) (*net.UnixListener, error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("rpc: bind %s: %w", path, err)
	}
	// Close removes the socket file. A SIGKILL leaves it, and the next
	// start's PrepareSocket removes it.
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}
