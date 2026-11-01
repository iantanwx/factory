package pack_test

import (
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/pack"
)

// C7: the kernel still pins impeccable, and the manifest marks its
// directory conditional on the project config key ui.
func TestC7ThePinStaysAndTheManifestMarksIt(t *testing.T) {
	if !slices.ContainsFunc(pack.ThirdPartyPins(), func(p pack.ThirdParty) bool { return p.Name == "impeccable" }) {
		t.Fatal("the kernel no longer pins impeccable")
	}
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Conditional, map[string]pack.Condition{"loop-impeccable": {Key: "ui"}}; !maps.Equal(got, want) {
		t.Fatalf("conditional = %v, want %v", got, want)
	}
	p, _, err := pack.Brief(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Conditional, map[string]string{"loop-impeccable": "ui"}; !maps.Equal(got, want) {
		t.Fatalf("the pack's conditional = %v, want %v", got, want)
	}
}

// withManifest is fixtureSource with its manifest replaced.
func withManifest(t *testing.T, manifest string) fstest.MapFS {
	t.Helper()
	src := fixtureSource(t, fstest.MapFS{}, "impeccable")
	src[pack.ManifestFile] = &fstest.MapFile{Data: []byte(manifest)}
	return src
}

// C8: a conditional entry whose skill the layout does not hold, or whose
// key no project config can set, fails the pack and names the entry. A
// skill directory and a pin's directory can each be marked.
func TestC8AMarkNoCheckoutCanHonourFailsThePack(t *testing.T) {
	const runbooks = `"runbooks": {"implementer": {"skills": ["loop-sdlc-turn"]}}, "skills": ["loop-sdlc-turn"]`
	for _, c := range []struct{ conditional, entry string }{
		{`{"loop-nothing": {"key": "ui"}}`, "loop-nothing"},
		{`{"impeccable": {"key": "ui"}}`, "impeccable"},
		{`{"loop-impeccable": {"key": "nothing"}}`, "loop-impeccable"},
		{`{"loop-impeccable": {"key": ""}}`, "loop-impeccable"},
	} {
		src := withManifest(t, `{`+runbooks+`, "conditional": `+c.conditional+`}`)
		if _, err := pack.ReadManifest(src); err == nil || !strings.Contains(err.Error(), `"`+c.entry+`"`) {
			t.Errorf("%s: err = %v, want a refusal that names %q", c.conditional, err, c.entry)
		}
		if _, err := pack.Layout(src); err == nil {
			t.Errorf("%s: the pack lays out", c.conditional)
		}
	}
	src := withManifest(t, `{`+runbooks+`, "conditional": {"loop-impeccable": {"key": "ui"}, "loop-sdlc-turn": {"key": "health"}}}`)
	if _, err := pack.ReadManifest(src); err != nil {
		t.Fatalf("a pin and a skill directory marked on two keys: %v", err)
	}
}

// unbound lists each line of a Markdown file in tree that names a
// conditional skill and none of the brief fields that say whether the run
// holds it. A conditional skill's own files are not read.
func unbound(t *testing.T, tree fs.FS, conditional []string) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(tree, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".md" {
			return err
		}
		if slices.Contains(conditional, strings.SplitN(name, "/", 2)[0]) {
			return nil
		}
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			names := slices.ContainsFunc(conditional, func(s string) bool { return strings.Contains(line, s) })
			bound := strings.Contains(line, "brief.json") || strings.Contains(line, "`skills`") || strings.Contains(line, "design_skill")
			if names && !bound {
				out = append(out, fmt.Sprintf("%s:%d: %s", name, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// C10: no kernel runbook or skill reads a conditional skill on a test of
// the repository. Each line that names one also names brief.json, skills
// or design_skill, the fields that say what the run holds.
func TestC10NoKernelTextTestsTheRepositoryToReadAConditionalSkill(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	conditional := slices.Sorted(maps.Keys(m.Conditional))
	if len(conditional) == 0 {
		t.Fatal("the manifest marks no skill conditional")
	}
	tree, err := pack.Layout(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if lines := unbound(t, tree, conditional); len(lines) > 0 {
		t.Fatalf("kernel lines read a conditional skill on a repository test:\n%s", strings.Join(lines, "\n"))
	}
}

// C10: the check refuses a runbook that reads loop-impeccable on a test of
// the repository, and passes one that reads the brief.
func TestC10ARunbookThatTestsTheRepositoryFails(t *testing.T) {
	conditional := []string{"loop-impeccable"}
	bad := fstest.MapFS{"loop-implementer/SKILL.md": {Data: []byte("# Implementer\n\nRead `loop-impeccable` when the repository holds a `.tsx` file.\n")}}
	if lines := unbound(t, bad, conditional); len(lines) != 1 {
		t.Fatalf("unbound = %v, want the one line", lines)
	}
	good := fstest.MapFS{
		"loop-implementer/SKILL.md": {Data: []byte("Read `loop-impeccable` when `skills` in `.loop/brief.json` lists it.\n")},
		"loop-impeccable/SKILL.md":  {Data: []byte("Use loop-impeccable on every screen.\n")},
	}
	if lines := unbound(t, good, conditional); len(lines) != 0 {
		t.Fatalf("unbound = %v, want none", lines)
	}
}
