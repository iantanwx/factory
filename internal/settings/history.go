package settings

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
)

// noBlob is the blob id git prints for no file.
const noBlob = "0000000000000000000000000000000000000000"

// change is one commit that changed the project file: its blob on the
// commit's first parent, and on the commit. noBlob is no file.
type change struct{ commit, before, after string }

// history is the project file's history in one git directory, newest
// first. It reads each blob once.
type history struct {
	dir   string
	log   []change
	blobs map[string][]byte
}

// readHistory lists the commits from rev back that changed the project
// file. git's history simplification names the branch commit of a merged
// change, and lists a merge only when it changed the file itself.
func readHistory(ctx context.Context, dir, rev string) (*history, error) {
	out, err := gitx.Run(ctx, dir, "log", "--format=%x00%H", "--raw", "--no-abbrev", "--cc",
		rev, "--", config.ProjectConfigPath)
	if err != nil {
		return nil, fmt.Errorf("settings: read the history of %s: %w", config.ProjectConfigPath, err)
	}
	h := &history{dir: dir, blobs: map[string][]byte{}}
	for _, entry := range strings.Split(string(out), "\x00")[1:] {
		lines := strings.Split(strings.TrimSpace(entry), "\n")
		c := change{commit: lines[0]}
		for _, l := range lines[1:] {
			// One colon for each parent, a mode for each parent and the
			// commit, then a blob for each: ":<mode> <mode> <before> <after> M".
			rest := strings.TrimLeft(l, ":")
			n, f := len(l)-len(rest), strings.Fields(rest)
			if n > 0 && len(f) > 2*n+1 {
				c.before, c.after = f[n+1], f[2*n+1]
			}
		}
		if c.after != "" {
			h.log = append(h.log, c)
		}
	}
	return h, nil
}

func (h *history) blob(ctx context.Context, id string) ([]byte, error) {
	if id == noBlob {
		return nil, nil
	}
	if b, ok := h.blobs[id]; ok {
		return b, nil
	}
	b, err := gitx.Run(ctx, h.dir, "cat-file", "blob", id)
	if err != nil {
		return nil, fmt.Errorf("settings: read %s blob %s: %w", config.ProjectConfigPath, id, err)
	}
	h.blobs[id] = b
	return b, nil
}

// from drops the changes newer than the newest one whose file holds raw,
// the bytes in force. With none, no change remains.
func (h *history) from(ctx context.Context, raw []byte) error {
	for i, c := range h.log {
		b, err := h.blob(ctx, c.after)
		if err != nil {
			return err
		}
		if bytes.Equal(b, raw) {
			h.log = h.log[i:]
			return nil
		}
	}
	h.log = nil
	return nil
}

// commits names, for each key of want, the newest change whose file sets
// the key to its value in want and whose parent's file does not. values
// reads the project values of one file.
func (h *history) commits(ctx context.Context, want map[string]string, values func([]byte) map[string]string) (map[string]string, error) {
	out := map[string]string{}
	of := map[string]map[string]string{}
	read := func(id string) (map[string]string, error) {
		if v, ok := of[id]; ok {
			return v, nil
		}
		b, err := h.blob(ctx, id)
		if err != nil {
			return nil, err
		}
		of[id] = values(b)
		return of[id], nil
	}
	for _, c := range h.log {
		if len(out) == len(want) {
			break
		}
		after, err := read(c.after)
		if err != nil {
			return nil, err
		}
		before, err := read(c.before)
		if err != nil {
			return nil, err
		}
		for key, v := range want {
			_, done := out[key]
			av, set := after[key]
			bv, was := before[key]
			if !done && set && av == v && (!was || bv != v) {
				out[key] = c.commit
			}
		}
	}
	return out, nil
}
