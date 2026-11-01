package gates

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

// C18, C19: the spec gate reads a spec under docs/specs/ and one under
// docs/plans/.
func TestC18C19SpecGateReadsBothDirectories(t *testing.T) {
	good := goodSpec(t)
	r := testRepo(t, map[string]string{
		"docs/specs/loop-a3f8-engine-cache-spec.md": good,
		"docs/specs/loop-b1-other-spec.md":          good,
		"docs/plans/loop-b2-other-spec.md":          good,
		"docs/specs/loop-a3f8-engine-notes.md":      "not a spec",
	})
	got, err := Spec(context.Background(), r)
	if err != nil || len(got) != 2 || got[0].File != "docs/plans/loop-b2-other-spec.md" || got[1].File != "docs/specs/loop-b1-other-spec.md" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, f := range got {
		if f.Rule != "name" || !strings.Contains(f.Message, "docs/specs/<epic id>-<slug>-spec.md") {
			t.Fatalf("finding = %+v, want rule name with the docs/specs/ hint", f)
		}
	}
}

// C18, C19: the prose gate reads a spec under docs/specs/ and one under
// docs/plans/.
func TestC18C19ProseGateReadsBothDirectories(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
	const slop = "# Spec\n\nIn order to utilize the cache, the reader leverages a robust and seamless approach.\n"
	for _, spec := range []string{"docs/specs/loop-a3f8-engine-cache-spec.md", "docs/plans/loop-a3f8-engine-cache-spec.md"} {
		r := testRepo(t, map[string]string{spec: slop})
		var out bytes.Buffer
		r.Out = &out
		got, err := Prose(context.Background(), r)
		if err != nil || len(got) != 2 || got[0].Rule != "ste" || got[1].Rule != "unslop" {
			t.Fatalf("%s: got %v %v", spec, got, err)
		}
		if !strings.Contains(out.String(), spec+":3") {
			t.Fatalf("%s: the linters named no line of the spec:\n%s", spec, out.String())
		}
	}
}

// C18, C19: the repair rule reads the documents under docs/specs/ and
// docs/plans/.
func TestC18C19RepairRuleReadsBothDirectories(t *testing.T) {
	for _, f := range []string{"docs/specs/x.md", "docs/specs/a/b.md", "docs/plans/x.md", "docs/plans/a/b.md"} {
		got := runbooks(t, map[string]string{f: "Never rerun the job, so the evidence stays clean.\n"})
		if len(got) != 1 || got[0].Rule != "repair" || got[0].File != f {
			t.Fatalf("%s: got %v", f, got)
		}
	}
}
