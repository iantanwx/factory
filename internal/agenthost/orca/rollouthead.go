package orca

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The head cache ports the rollout census of alpha's codex-sessions.ts and
// codex-census.ts. A Codex rollout names its checkout only inside the file,
// so every tick of every Codex session read the first record of each
// rollout in its window, up to 33 days of other sessions' rollouts. Almost
// every one ended days ago, and its bytes have not changed since the last
// tick read them. The cache keeps the cwd that each head names, by file
// identity, so a tick reads only the heads that changed.
//
// The host lives as long as the server, so the cache lives in memory. alpha
// also writes its census to disk because each of its ticks is a new process.
//
// It holds no transcript and no finish verdict. The rollouts that name the
// checkout are still read whole at every tick.
type rolloutHeads struct {
	mu   sync.Mutex
	dirs map[string]*headDir
}

type headDir struct {
	used  time.Time
	files map[string]rolloutHead
}

type rolloutHead struct {
	id  rolloutID
	cwd string
}

// rolloutID is what the cache compares: device, inode, size, and the
// nanosecond modification and change times. A write to a rollout moves its
// change time, and no process of this user can set a change time back.
type rolloutID struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64
}

// headSettle is the age a change time must reach before its head is reused.
// A rewrite within one tick of the filesystem clock can keep the change
// time, so a younger file reads again.
const headSettle = time.Second

// headIdle drops the entries of a day directory that no tick listed for
// this long. A dropped entry costs one read.
const headIdle = 48 * time.Hour

func statRollout(path string) (rolloutID, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return rolloutID{}, err
	}
	// Dev is uint64 on Linux and int32 on darwin.
	return rolloutID{
		dev: uint64(st.Dev), ino: st.Ino, size: st.Size,
		mtime: st.Mtim.Nano(), ctime: st.Ctim.Nano(),
	}, nil
}

// keep forgets the files of dir that the listing no longer holds, and the
// directories that no tick listed for headIdle.
func (c *rolloutHeads) keep(dir string, names []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirs == nil {
		c.dirs = map[string]*headDir{}
	}
	old := c.dirs[dir]
	d := &headDir{used: now, files: map[string]rolloutHead{}}
	if old != nil {
		for _, n := range names {
			if e, ok := old.files[n]; ok {
				d.files[n] = e
			}
		}
	}
	c.dirs[dir] = d
	for k, v := range c.dirs {
		if now.Sub(v.used) > headIdle {
			delete(c.dirs, k)
		}
	}
}

func (c *rolloutHeads) get(dir, name string, id rolloutID) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.dirs[dir]
	if d == nil {
		return "", false
	}
	e, ok := d.files[name]
	return e.cwd, ok && e.id == id
}

func (c *rolloutHeads) put(dir, name string, e rolloutHead) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.dirs[dir]; d != nil {
		d.files[name] = e
	}
}

// codexNames says whether the session_meta record that opens the rollout
// names the checkout. It reuses an earlier read while the file's identity
// holds and its change time is older than headSettle.
func (h *Host) codexNames(dir, name, checkout string) bool {
	path := filepath.Join(dir, name)
	before, err := statRollout(path)
	if err != nil {
		return false
	}
	settled := h.wall().Add(-headSettle).UnixNano()
	if cwd, ok := h.heads.get(dir, name, before); ok && before.ctime < settled {
		return cwd == checkout
	}
	cwd, err := h.codexCwd(path)
	if err != nil {
		return false
	}
	// A file that changed during the read, or that is too young, reads again.
	if after, err := statRollout(path); err == nil && after == before && before.ctime < settled {
		h.heads.put(dir, name, rolloutHead{id: before, cwd: cwd})
	}
	return cwd == checkout
}

// codexCwd reads only the first record of a rollout, and answers the cwd it
// names when it is session_meta, or "". An error reads no answer, and the
// cache keeps none.
func (h *Host) codexCwd(path string) (string, error) {
	h.headReads.Add(1)
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReaderSize(f, 1<<20).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "session_meta" {
		return "", nil
	}
	return filepath.Clean(r.Payload.Cwd), nil
}
