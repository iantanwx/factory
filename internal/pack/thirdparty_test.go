package pack_test

import (
	"fmt"
	"io/fs"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/iantanwx/loop/internal/pack"
)

const (
	goodCommit = "0123456789abcdef0123456789abcdef01234567"
	goodSHA    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func pin(name, path string) string {
	return fmt.Sprintf(`{"name": %q, "repo": "owner/repo", "commit": %q, "path": %q, "sha256": %q}`, name, goodCommit, path, goodSHA)
}

func pins(entries ...string) fstest.MapFS {
	return fstest.MapFS{pack.ThirdPartyFile: {Data: []byte(`{"skills": [` + strings.Join(entries, ", ") + `]}`)}}
}

func TestReadThirdPartyReadsAGoodPin(t *testing.T) {
	got, err := pack.ReadThirdParty(pins(pin("neon-postgres", "skills/neon-postgres"), pin("asd-ste100", "")))
	if err != nil {
		t.Fatal(err)
	}
	want := []pack.ThirdParty{
		{Name: "neon-postgres", Repo: "owner/repo", Commit: goodCommit, Path: "skills/neon-postgres", SHA256: goodSHA},
		{Name: "asd-ste100", Repo: "owner/repo", Commit: goodCommit, Path: "", SHA256: goodSHA},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestC5ABadPinFailsAtLoad proves C5: ReadThirdParty refuses each pin
// that breaks an invariant of the domain model, and the error names it.
func TestC5ABadPinFailsAtLoad(t *testing.T) {
	cases := []struct {
		name, file, names string
	}{
		{"unknown pin field", `{"skills": [{"name": "neon", "repo": "o/r", "commit": "` + goodCommit + `", "path": "", "sha256": "` + goodSHA + `", "tag": "v1"}]}`, "neon"},
		{"unknown file field", `{"skills": [], "extra": true}`, "extra"},
		{"empty name", `{"skills": [` + pin("", "") + `]}`, "pin 0"},
		{"upper-case name", `{"skills": [` + pin("Neon", "") + `]}`, "Neon"},
		{"name in the loop- namespace", `{"skills": [` + pin("loop-neon", "") + `]}`, "loop-neon"},
		{"short commit", `{"skills": [{"name": "neon", "repo": "o/r", "commit": "abc123", "path": "", "sha256": "` + goodSHA + `"}]}`, "neon"},
		{"commit not hex", `{"skills": [{"name": "neon", "repo": "o/r", "commit": "` + strings.Repeat("g", 40) + `", "path": "", "sha256": "` + goodSHA + `"}]}`, "neon"},
		{"short sha256", `{"skills": [{"name": "neon", "repo": "o/r", "commit": "` + goodCommit + `", "path": "", "sha256": "` + goodSHA[:63] + `"}]}`, "neon"},
		{"sha256 not hex", `{"skills": [{"name": "neon", "repo": "o/r", "commit": "` + goodCommit + `", "path": "", "sha256": "` + strings.Repeat("z", 64) + `"}]}`, "neon"},
		{"two pins with one name", `{"skills": [` + pin("neon", "a") + `, ` + pin("neon", "b") + `]}`, "neon"},
		{"absolute path", `{"skills": [` + pin("neon", "/skills/neon") + `]}`, "neon"},
		{"path that leaves the repository", `{"skills": [` + pin("neon", "../neon") + `]}`, "neon"},
		{"path with .. inside", `{"skills": [` + pin("neon", "skills/../../neon") + `]}`, "neon"},
		{"path that is not clean", `{"skills": [` + pin("neon", "skills//neon/") + `]}`, "neon"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pack.ReadThirdParty(fstest.MapFS{pack.ThirdPartyFile: {Data: []byte(c.file)}})
			if err == nil {
				t.Fatalf("want a refusal, got pins %+v", got)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Fatalf("the refusal does not name %q: %v", c.names, err)
			}
		})
	}
}

// TestC5LayoutRefusesAPinOnARunbookOrSkillDirectory proves the last case
// of C5: loop-<name> of a pin is no runbook's and no first-party skill's
// directory.
func TestC5LayoutRefusesAPinOnARunbookOrSkillDirectory(t *testing.T) {
	base := func(p string) fstest.MapFS {
		src := pins(p)
		src["manifest.json"] = &fstest.MapFile{Data: []byte(`{"runbooks": {"implementer": {"skills": ["loop-sdlc-turn"]}}, "skills": ["loop-sdlc-turn"]}`)}
		src["runbooks/implementer.md"] = &fstest.MapFile{Data: []byte("x")}
		src["skills/loop-sdlc-turn/SKILL.md"] = &fstest.MapFile{Data: []byte("y")}
		return src
	}
	for _, name := range []string{"implementer", "sdlc-turn"} {
		t.Run(name, func(t *testing.T) {
			_, err := pack.Layout(base(pin(name, "")))
			if err == nil || !strings.Contains(err.Error(), `"`+name+`"`) || !strings.Contains(err.Error(), "loop-"+name) {
				t.Fatalf("want a refusal that names pin %q and loop-%s, got %v", name, name, err)
			}
		})
	}
	if _, err := pack.Layout(base(pin("neon", "skills/neon"))); err != nil {
		t.Fatalf("a pin on its own directory lays out: %v", err)
	}
}

func TestShippedThirdPartyPinsLoad(t *testing.T) {
	got, err := pack.ReadThirdParty(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, pack.ThirdPartyPins()) {
		t.Fatalf("ThirdPartyPins %+v differs from the embedded file %+v", pack.ThirdPartyPins(), got)
	}
}

// TestC6TreeHashOfAFixedTree pins the encoding: the literal was computed
// outside Go, as sha256 over
// "SKILL.md\x00644\x0019\x00<bytes>scripts/run\x00755\x0018\x00<bytes>".
func TestC6TreeHashOfAFixedTree(t *testing.T) {
	tree := fstest.MapFS{
		"SKILL.md":    {Data: []byte("---\nname: demo\n---\n"), Mode: 0o644},
		"scripts":     {Mode: fs.ModeDir | 0o700},
		"scripts/run": {Data: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
	}
	got, err := pack.TreeHash(tree)
	if err != nil {
		t.Fatal(err)
	}
	const want = "f8ea86a039e5bf54a12cd73ea4a3e14478efed921a32e071ccac0a93fe4864cf"
	if got != want {
		t.Fatalf("TreeHash = %s, want %s", got, want)
	}
}

func TestTreeHashRefusesALink(t *testing.T) {
	tree := fstest.MapFS{
		"SKILL.md": {Data: []byte("x"), Mode: 0o644},
		"link":     {Data: []byte("SKILL.md"), Mode: fs.ModeSymlink | 0o777},
	}
	if _, err := pack.TreeHash(tree); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("want a refusal that names link, got %v", err)
	}
}

// shuffled serves a tree whose directories list their entries in a
// random order, as a tar archive can hold its members.
type shuffled struct {
	fstest.MapFS
	rng *rand.Rand
}

func (s shuffled) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := s.MapFS.ReadDir(name)
	s.rng.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	return entries, err
}

// genTree returns a tree of 1 to 6 regular files, some nested and some
// executable, with explicit directory entries.
func genTree(rng *rand.Rand) fstest.MapFS {
	tree := fstest.MapFS{}
	n := 1 + rng.IntN(6)
	for len(tree) < n {
		name := genPath(rng)
		if isPrefixOfAny(tree, name) {
			continue
		}
		data := make([]byte, rng.IntN(64))
		for i := range data {
			data[i] = byte(rng.IntN(256))
		}
		mode := fs.FileMode(0o644)
		if rng.IntN(3) == 0 {
			mode = 0o755
		}
		tree[name] = &fstest.MapFile{Data: data, Mode: mode, ModTime: time.Unix(rng.Int64N(1<<31), 0)}
	}
	for name := range tree {
		for dir := parent(name); dir != "."; dir = parent(dir) {
			tree[dir] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
		}
	}
	return tree
}

func genPath(rng *rand.Rand) string {
	parts := make([]string, 1+rng.IntN(3))
	for i := range parts {
		parts[i] = []string{"a", "b", "a-b", "SKILL.md", "x.sh", "ref"}[rng.IntN(6)]
	}
	return strings.Join(parts, "/")
}

func parent(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return "."
}

// isPrefixOfAny reports whether name and a file of tree would need one
// path to be both a file and a directory.
func isPrefixOfAny(tree fstest.MapFS, name string) bool {
	for f := range tree {
		if f == name || strings.HasPrefix(f, name+"/") || strings.HasPrefix(name, f+"/") {
			return true
		}
	}
	return false
}

func files(tree fstest.MapFS) []string {
	var out []string
	for name, f := range tree {
		if !f.Mode.IsDir() {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func clone(tree fstest.MapFS) fstest.MapFS {
	out := fstest.MapFS{}
	for name, f := range tree {
		c := *f
		c.Data = slices.Clone(f.Data)
		out[name] = &c
	}
	return out
}

func mustHash(t *testing.T, tree fs.FS) string {
	t.Helper()
	h, err := pack.TreeHash(tree)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestC6TreeHashIsTheTreesIdentity proves C6 over 500 generated trees:
// each change to a file's bytes, path, presence or executable bit
// changes the hash, and member order, timestamps, directory modes and
// other mode bits do not.
func TestC6TreeHashIsTheTreesIdentity(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 0x5eed))
	changes := map[string]func(fstest.MapFS, string){
		"bytes": func(tr fstest.MapFS, f string) { tr[f].Data = append(tr[f].Data, byte(rng.IntN(256))) },
		"flipped byte": func(tr fstest.MapFS, f string) {
			if len(tr[f].Data) == 0 {
				tr[f].Data = []byte{0}
				return
			}
			tr[f].Data[rng.IntN(len(tr[f].Data))] ^= 1 << rng.IntN(8)
		},
		"path": func(tr fstest.MapFS, f string) {
			tr[f+".renamed"] = tr[f]
			delete(tr, f)
		},
		"added": func(tr fstest.MapFS, f string) {
			tr[f+".new"] = &fstest.MapFile{Mode: 0o644}
		},
		"deleted":  func(tr fstest.MapFS, f string) { delete(tr, f) },
		"exec bit": func(tr fstest.MapFS, f string) { tr[f].Mode ^= 0o111 },
	}
	kept := map[string]func(fstest.MapFS, string){
		"timestamps": func(tr fstest.MapFS, _ string) {
			for _, f := range tr {
				f.ModTime = time.Unix(rng.Int64N(1<<31), 0)
			}
		},
		"directory modes": func(tr fstest.MapFS, _ string) {
			for _, f := range tr {
				if f.Mode.IsDir() {
					f.Mode = fs.ModeDir | fs.FileMode(rng.IntN(0o1000))
				}
			}
		},
		"non-executable mode bits": func(tr fstest.MapFS, f string) {
			tr[f].Mode ^= fs.FileMode(1+rng.IntN(0o777)) &^ 0o111
		},
		"one executable bit for another": func(tr fstest.MapFS, f string) {
			if tr[f].Mode&0o111 != 0 {
				tr[f].Mode = tr[f].Mode&^0o111 | fs.FileMode(1<<(3*rng.IntN(3)))
			}
		},
	}
	for i := range 500 {
		tree := genTree(rng)
		want := mustHash(t, tree)
		names := files(tree)
		target := names[rng.IntN(len(names))]
		if got := mustHash(t, shuffled{tree, rng}); got != want {
			t.Fatalf("tree %d: member order changed the hash", i)
		}
		for kind, change := range changes {
			tr := clone(tree)
			change(tr, target)
			if mustHash(t, tr) == want {
				t.Fatalf("tree %d: a change of %s to %s kept the hash %s", i, kind, target, want)
			}
		}
		for kind, change := range kept {
			tr := clone(tree)
			change(tr, target)
			if got := mustHash(t, tr); got != want {
				t.Fatalf("tree %d: a change of %s to %s changed the hash", i, kind, target)
			}
		}
	}
}
