package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// PendingEntry is one write that the CLI sent and that has no definitive
// answer yet (KTD13). A repeat of the same verb, arguments and content
// while the entry stands reuses its key.
type PendingEntry struct {
	Key         string            `json:"key"`
	Verb        string            `json:"verb"`
	Args        []string          `json:"args"`
	Flags       map[string]string `json:"flags"`
	ArgsHash    string            `json:"args_hash"`
	ContentHash string            `json:"content_hash"`
	// ExpectedStatus is the status the first send read. A repeat sends it
	// again, so a retry after another actor's move refuses (AE4).
	ExpectedStatus string    `json:"expected_status,omitempty"`
	Created        time.Time `json:"created"`
}

// Pending is the pending-write log of one checkout: .loop/pending.jsonl.
// An flock on .loop/pending.lock orders CLIs that share the checkout.
type Pending struct {
	dir string
}

// OpenPending returns the log of checkout.
func OpenPending(checkout string) *Pending {
	return &Pending{dir: filepath.Join(checkout, LoopDir)}
}

func (p *Pending) path() string { return filepath.Join(p.dir, PendingFileName) }

// lock holds the checkout's pending lock while fn runs.
func (p *Pending) lock(fn func() error) error {
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.dir, "pending.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock the pending log: %w", err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return fn()
}

// Entries returns every pending entry in the order they were added. A line
// that does not parse is skipped, so a torn last line from a killed CLI
// loses only itself.
func (p *Pending) Entries() ([]PendingEntry, error) {
	var out []PendingEntry
	err := p.lock(func() error {
		var err error
		out, err = p.read()
		return err
	})
	return out, err
}

func (p *Pending) read() ([]PendingEntry, error) {
	data, err := os.ReadFile(p.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []PendingEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e PendingEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Key != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Reserve returns the key of a pending entry with the same verb, arguments
// and content hash. When none stands, it derives a fresh key with derive,
// appends the entry and syncs the log before it returns, so the entry
// survives a kill after the send.
func (p *Pending) Reserve(e PendingEntry, derive func() (string, error)) (key string, reused bool, err error) {
	err = p.lock(func() error {
		entries, err := p.read()
		if err != nil {
			return err
		}
		for _, have := range entries {
			if have.Verb == e.Verb && have.ArgsHash == e.ArgsHash && have.ContentHash == e.ContentHash {
				key, reused = have.Key, true
				return nil
			}
		}
		if key, err = derive(); err != nil {
			return err
		}
		e.Key = key
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(p.path(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	})
	return key, reused, err
}

// Clear removes the entry with key. The CLI calls it on a definitive
// answer: exit 0 or exit 3.
func (p *Pending) Clear(key string) error {
	return p.lock(func() error {
		entries, err := p.read()
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		kept := 0
		for _, e := range entries {
			if e.Key == key {
				continue
			}
			line, err := json.Marshal(e)
			if err != nil {
				return err
			}
			buf.Write(line)
			buf.WriteByte('\n')
			kept++
		}
		if kept == len(entries) {
			return nil
		}
		tmp, err := os.CreateTemp(p.dir, ".pending-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(tmp.Name()) }()
		if _, err := tmp.Write(buf.Bytes()); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), p.path())
	})
}
