package admission

import (
	"context"
	"slices"
	"testing"
)

func TestDeclaredPaths(t *testing.T) {
	body := "Do it. Read `docs/adr/0006-split.md` and docs/plans/x.md first.\n" +
		"See https://example.com/docs/y.md and README.md.\n\n" +
		"## Files\n\n" +
		"**May change:**\n" +
		"- `internal/admission/` and `go.mod`\n" +
		"- `cmd/loop/{main,serve}.go`\n" +
		"- `internal/**/*_test.go`\n\n" +
		"**Must not change:**\n" +
		"- `proto/`\n\n" +
		"## Notes\n\n- `ignored/here.go`\n"
	want := []string{
		"cmd/loop/main.go", "cmd/loop/serve.go", "docs/adr/0006-split.md", "docs/plans/x.md",
		"go.mod", "internal/", "internal/admission/",
	}
	if got := DeclaredPaths(body); !slices.Equal(got, want) {
		t.Fatalf("DeclaredPaths = %v, want %v", got, want)
	}
	inline := "## Files\n\nMay change: `app/x.go`\n"
	if got := DeclaredPaths(inline); !slices.Equal(got, []string{"app/x.go"}) {
		t.Fatalf("an inline label = %v", got)
	}
	if got := DeclaredPaths("no files section"); len(got) != 0 {
		t.Fatalf("a body with no paths = %v", got)
	}
}

// Parity: "retry input changes with task and declared source, not
// unrelated main commits or display metadata" (admission-input.test.ts,
// alpha). The machinery tree, the PR head and the CI state no longer enter
// the identity.
func TestInputsHash(t *testing.T) {
	ctx := context.Background()
	base := newFakeBase(nil)
	task := Task{Title: "item", Body: declaredBody}
	in := func(t Task, release string) string {
		h, err := computeInputs(ctx, readOnly{base}, "alpha", t, release)
		if err != nil {
			panic(err)
		}
		return h
	}
	first := in(task, "v1")
	base.set("unrelated.txt", "changed")
	if in(task, "v1") != first {
		t.Fatal("an unrelated file changed the identity")
	}
	base.set("app/task.go", "changed")
	second := in(task, "v1")
	if second == first {
		t.Fatal("a declared file did not change the identity")
	}
	if in(Task{Title: "item", Body: declaredBody + "\nChanged contract"}, "v1") == second {
		t.Fatal("a body edit did not change the identity")
	}
	if in(Task{Title: "renamed", Body: declaredBody}, "v1") == second {
		t.Fatal("a title edit did not change the identity")
	}
	if in(task, "v2") == second {
		t.Fatal("a release did not change the identity")
	}
	if _, err := computeInputs(ctx, nil, "alpha", task, "v1"); err == nil {
		t.Fatal("declared paths with no base reader passed")
	}
	if _, err := computeInputs(ctx, nil, "alpha", Task{Title: "x", Body: "no paths"}, "v1"); err != nil {
		t.Fatalf("a task with no paths needs no base: %v", err)
	}
}

// readOnly skips the lock probe of fakeBase, which needs a store.
type readOnly struct{ b *fakeBase }

func (r readOnly) Files(_ context.Context, _ string, prefixes []string) (map[string]string, error) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	out := map[string]string{}
	for p, id := range r.b.files {
		for _, pre := range prefixes {
			if len(p) >= len(pre) && p[:len(pre)] == pre {
				out[p] = id
			}
		}
	}
	return out, nil
}
