package brief

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
)

// impeccablePack is testPack with a loop-impeccable directory.
func impeccablePack() Pack {
	p := testPack()
	tree := fstest.MapFS{"loop-impeccable/SKILL.md": {Data: []byte("# impeccable\n")}}
	for name, f := range p.FS.(fstest.MapFS) {
		tree[name] = f
	}
	p.FS = tree
	return p
}

// skillRootDirs lists the loop-* directories under the checkout's skill
// root, sorted.
func skillRootDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, SkillRoot(agenthost.Claude)))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), SkillPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// injectBrief injects p into dir and returns the brief.json it wrote.
func injectBrief(t *testing.T, dir string, p Pack) Brief {
	t.Helper()
	if _, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Claude, Pack: p, Brief: testBrief()}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// C9: brief.json lists each loop-* directory the run holds, and only
// those. A pack without loop-impeccable writes no such directory, clears
// the one an earlier injection wrote, and keeps the whole pack's hash.
func TestC9TheBriefListsWhatTheRunHolds(t *testing.T) {
	dir := gitRepo(t)
	whole := injectBrief(t, dir, impeccablePack())
	want := []string{"loop-code-reviewer", "loop-impeccable", "loop-implementer"}
	if got := skillRootDirs(t, dir); !slices.Equal(got, want) {
		t.Fatalf("the whole pack wrote %v, want %v", got, want)
	}
	if !slices.Equal(whole.Skills, want) {
		t.Fatalf("the whole pack's skills = %v, want %v", whole.Skills, want)
	}

	without := injectBrief(t, dir, impeccablePack().Without("loop-impeccable"))
	want = []string{"loop-code-reviewer", "loop-implementer"}
	if got := skillRootDirs(t, dir); !slices.Equal(got, want) {
		t.Fatalf("the pack without loop-impeccable left %v, want %v", got, want)
	}
	if !slices.Equal(without.Skills, want) {
		t.Fatalf("the pack without loop-impeccable lists %v, want %v", without.Skills, want)
	}
	if without.PackHash == "" || without.PackHash != whole.PackHash {
		t.Fatalf("pack_hash %q without loop-impeccable, %q whole", without.PackHash, whole.PackHash)
	}
}

// C9: a brief whose pack holds no skill still holds an empty skills list.
func TestC9AnEmptyPackListsNoSkill(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Inject(context.Background(), Injection{Checkout: dir, Harness: agenthost.Claude, Pack: Pack{Version: "v1"}, Brief: testBrief()}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"skills": []`) {
		t.Fatalf("brief.json holds no empty skills list:\n%s", raw)
	}
}
