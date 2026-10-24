package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/pack"
)

// member is one entry of a test archive.
type member struct {
	name string
	typ  byte
	mode int64
	body string
	link string
}

// skillArchive builds a gzip tar archive in the shape GitHub serves: a pax
// global header, then every member under one top directory.
func skillArchive(t *testing.T, members ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	global := &tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": strings.Repeat("c", 40)}}
	if err := tw.WriteHeader(global); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		h := &tar.Header{Name: m.name, Typeflag: m.typ, Mode: m.mode, Linkname: m.link, Size: int64(len(m.body))}
		if m.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const demoSkill = "---\nname: demo\ndescription: A demo skill.\n---\n\nDo the demo.\n"

// demoMembers is a repository whose skill demo sits at skills/demo, with
// files outside the skill path beside it.
func demoMembers(skill string) []member {
	return []member{
		{name: "repo-abc/", typ: tar.TypeDir, mode: 0o775},
		{name: "repo-abc/LICENSE", typ: tar.TypeReg, mode: 0o664, body: "MIT\n"},
		{name: "repo-abc/skills/", typ: tar.TypeDir, mode: 0o775},
		{name: "repo-abc/skills/demo/", typ: tar.TypeDir, mode: 0o775},
		{name: "repo-abc/skills/demo/SKILL.md", typ: tar.TypeReg, mode: 0o664, body: skill},
		{name: "repo-abc/skills/demo/scripts/run", typ: tar.TypeReg, mode: 0o775, body: "#!/bin/sh\necho demo\n"},
		{name: "repo-abc/skills/demo-other/SKILL.md", typ: tar.TypeReg, mode: 0o664, body: "---\nname: demo-other\n---\n"},
		{name: "repo-abc/skills/other/SKILL.md", typ: tar.TypeReg, mode: 0o664, body: "---\nname: other\n---\n"},
	}
}

// demoTree is the tree the demo pin holds.
func demoTree(skill string) fstest.MapFS {
	return fstest.MapFS{
		"SKILL.md":    {Data: []byte(skill), Mode: 0o644},
		"scripts/run": {Data: []byte("#!/bin/sh\necho demo\n"), Mode: 0o755},
	}
}

func treeHash(t *testing.T, tree fs.FS) string {
	t.Helper()
	h, err := pack.TreeHash(tree)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// skillWorld serves one archive for each pin name from a local server.
type skillWorld struct {
	archives map[string][]byte
	fetcher  SkillFetcher
}

func newSkillWorld(t *testing.T) *skillWorld {
	t.Helper()
	w := &skillWorld{archives: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		data, ok := w.archives[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(data)
	}))
	t.Cleanup(srv.Close)
	w.fetcher = SkillFetcher{HTTP: srv.Client(), URL: func(p pack.ThirdParty) string { return srv.URL + "/" + p.Name }}
	return w
}

func demoPin(sha string) pack.ThirdParty {
	return pack.ThirdParty{Name: "demo", Repo: "owner/repo", Commit: strings.Repeat("a", 40), Path: "skills/demo", SHA256: sha}
}

func TestC7FetchAcceptsThePinnedTree(t *testing.T) {
	w := newSkillWorld(t)
	w.archives["demo"] = skillArchive(t, demoMembers(demoSkill)...)
	root := []member{
		{name: "root-def/", typ: tar.TypeDir, mode: 0o775},
		{name: "root-def/SKILL.md", typ: tar.TypeReg, mode: 0o664, body: "---\nname: root\n---\n"},
	}
	w.archives["root"] = skillArchive(t, root...)
	rootTree := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: root\n---\n"), Mode: 0o644}}
	pins := []pack.ThirdParty{
		demoPin(treeHash(t, demoTree(demoSkill))),
		{Name: "root", Repo: "owner/root", Commit: strings.Repeat("b", 40), Path: "", SHA256: treeHash(t, rootTree)},
	}

	trees, err := w.fetcher.Fetch(context.Background(), pins)
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 {
		t.Fatalf("trees = %v", trees)
	}
	// The demo tree holds the skill's two files, and nothing from outside
	// skills/demo, not even skills/demo-other, whose name shares the prefix.
	var files []string
	err = fs.WalkDir(trees["demo"], ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != "SKILL.md,scripts/run" {
		t.Fatalf("demo files = %v", files)
	}
	if data, _ := fs.ReadFile(trees["demo"], "SKILL.md"); string(data) != demoSkill {
		t.Fatalf("SKILL.md = %q", data)
	}
	info, err := fs.Stat(trees["demo"], "scripts/run")
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("scripts/run mode = %v %v", info, err)
	}
	info, err = fs.Stat(trees["demo"], "SKILL.md")
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("SKILL.md mode = %v %v", info, err)
	}
	if data, _ := fs.ReadFile(trees["root"], "SKILL.md"); string(data) != "---\nname: root\n---\n" {
		t.Fatalf("root SKILL.md = %q", data)
	}
}

func TestC7FetchRefusesAnythingButThePinnedTree(t *testing.T) {
	good := treeHash(t, demoTree(demoSkill))
	under := func(m member) []member { return append(demoMembers(demoSkill), m) }
	cases := []struct {
		name     string
		archive  []member // nil serves no archive, so the server answers 404
		sha      string
		mismatch bool
		want     string
	}{
		{name: "a tree whose hash differs", sha: strings.Repeat("0", 64), mismatch: true, want: "tree hash",
			archive: demoMembers(demoSkill)},
		{name: "a changed file", sha: good, mismatch: true, want: "tree hash",
			archive: demoMembers(strings.Replace(demoSkill, "Do the demo.", "Do something else.", 1))},
		{name: "a front matter that does not name the skill", mismatch: true, want: "name: demo",
			sha:     treeHash(t, demoTree(strings.Replace(demoSkill, "name: demo", "name: other", 1))),
			archive: demoMembers(strings.Replace(demoSkill, "name: demo", "name: other", 1))},
		{name: "a SKILL.md with no front matter", mismatch: true, want: "name: demo",
			sha:     treeHash(t, demoTree("name: demo\n")),
			archive: demoMembers("name: demo\n")},
		{name: "a symlink", sha: good, want: "skills/demo/link",
			archive: under(member{name: "repo-abc/skills/demo/link", typ: tar.TypeSymlink, link: "SKILL.md"})},
		{name: "a hard link", sha: good, want: "skills/demo/hard",
			archive: under(member{name: "repo-abc/skills/demo/hard", typ: tar.TypeLink, link: "repo-abc/skills/demo/SKILL.md"})},
		{name: "a character device", sha: good, want: "skills/demo/tty",
			archive: under(member{name: "repo-abc/skills/demo/tty", typ: tar.TypeChar})},
		{name: "a block device", sha: good, want: "skills/demo/disk",
			archive: under(member{name: "repo-abc/skills/demo/disk", typ: tar.TypeBlock})},
		{name: "a fifo", sha: good, want: "skills/demo/pipe",
			archive: under(member{name: "repo-abc/skills/demo/pipe", typ: tar.TypeFifo})},
		{name: "a member that leaves the skill directory", sha: good, want: "leaves",
			archive: under(member{name: "repo-abc/skills/demo/../../../etc/passwd", typ: tar.TypeReg, mode: 0o644, body: "x"})},
		{name: "an absolute member", sha: good, want: "leaves",
			archive: under(member{name: "/etc/passwd", typ: tar.TypeReg, mode: 0o644, body: "x"})},
		{name: "a tree over 32 MiB", sha: good, want: "32 MiB",
			archive: under(member{name: "repo-abc/skills/demo/big", typ: tar.TypeReg, mode: 0o644, body: strings.Repeat("x", 32<<20)})},
		{name: "an archive without the skill path", sha: good, want: "skills/demo",
			archive: []member{{name: "repo-abc/README.md", typ: tar.TypeReg, mode: 0o644, body: "x"}}},
		{name: "a 404", sha: good, want: "404"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newSkillWorld(t)
			if tc.archive != nil {
				w.archives["demo"] = skillArchive(t, tc.archive...)
			}
			trees, err := w.fetcher.Fetch(context.Background(), []pack.ThirdParty{demoPin(tc.sha)})
			if err == nil {
				t.Fatal("the fetch accepted the archive")
			}
			if trees != nil {
				t.Fatalf("a refused fetch returned trees: %v", trees)
			}
			if errors.Is(err, ErrPinMismatch) != tc.mismatch {
				t.Fatalf("errors.Is(err, ErrPinMismatch) = %v, want %v: %v", !tc.mismatch, tc.mismatch, err)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "demo") {
				t.Fatalf("err = %v, want it to name %q and the pin", err, tc.want)
			}
		})
	}
}

func TestC7FetchReturnsNoTreeWhenALaterPinFails(t *testing.T) {
	w := newSkillWorld(t)
	w.archives["demo"] = skillArchive(t, demoMembers(demoSkill)...)
	pins := []pack.ThirdParty{demoPin(treeHash(t, demoTree(demoSkill))), demoPin(strings.Repeat("0", 64))}
	pins[1].Name = "missing"
	trees, err := w.fetcher.Fetch(context.Background(), pins)
	if err == nil || trees != nil {
		t.Fatalf("trees = %v, err = %v", trees, err)
	}
}

// TestThirdPartyPinsMatchUpstream fetches each embedded pin from GitHub and
// checks its tree hash. It reads the network, so it runs only when
// LOOP_FETCH_PINS is set: LOOP_FETCH_PINS=1 go test ./internal/release -run ThirdPartyPins
func TestThirdPartyPinsMatchUpstream(t *testing.T) {
	if os.Getenv("LOOP_FETCH_PINS") == "" {
		t.Skip("set LOOP_FETCH_PINS=1 to fetch the embedded pins from GitHub")
	}
	pins := pack.ThirdPartyPins()
	trees, err := SkillFetcher{}.Fetch(context.Background(), pins)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pins {
		if trees[p.Name] == nil {
			t.Errorf("no tree for %s", p.Name)
		}
		t.Logf("%s %s@%s %s", p.Name, p.Repo, p.Commit, p.SHA256)
	}
}
