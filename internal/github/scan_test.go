package github

import (
	"context"
	"slices"
	"testing"
)

const (
	prdPath = "docs/prd/e.md"
	prdBody = "a\nb\nc\nd\ne\n"
)

// editCommits reads the commits of the checkout's work branch since
// from, over the server's mirror, and lists those that edit the PRD.
func (e *env) editCommits(from, base string) []string {
	e.t.Helper()
	ctx := context.Background()
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	m := e.svc.Mirror(e.project)
	if err := m.Init(ctx); err != nil {
		e.t.Fatal(err)
	}
	if err := m.FetchCheckout(ctx, e.checkout, head, "refs/loop/incoming/test"); err != nil {
		e.t.Fatal(err)
	}
	commits, err := m.newCommits(ctx, head, base, from)
	if err != nil {
		e.t.Fatal(err)
	}
	edits, err := m.EditCommits(ctx, func(p string) bool { return p == prdPath }, base, commits)
	if err != nil {
		e.t.Fatal(err)
	}
	return edits
}

// C15: the scan counts each commit that changes the document. A commit
// that changes no document file and a merge of the base are no edit. A
// merge of any other branch that changes the document is one, and so is
// each commit it brings in.
func TestC15ScanCountsTheCommitsThatEditTheDocument(t *testing.T) {
	cases := []struct {
		name  string
		build func(e *env) []string
	}{
		{"one edit", func(e *env) []string {
			c := commit(e.t, e.checkout, "revise", map[string]string{prdPath: "two\n"})
			return []string{c}
		}},
		{"two edits", func(e *env) []string {
			a := commit(e.t, e.checkout, "revise", map[string]string{prdPath: "two\n"})
			b := commit(e.t, e.checkout, "revise again", map[string]string{prdPath: "three\n"})
			return []string{a, b}
		}},
		{"an edit and a commit outside the document", func(e *env) []string {
			c := commit(e.t, e.checkout, "revise", map[string]string{prdPath: "two\n"})
			commit(e.t, e.checkout, "notes", map[string]string{"notes.txt": "n\n"})
			return []string{c}
		}},
		{"an edit and a merge of the base that changes the document", func(e *env) []string {
			e.baseMoves(e.t, map[string]string{prdPath: prdBody})
			gitT(e.t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge main", "main")
			c := commit(e.t, e.checkout, "revise", map[string]string{prdPath: "A\nb\nc\nd\ne\n"})
			e.baseMoves(e.t, map[string]string{prdPath: "a\nb\nc\nd\nE\n"})
			gitT(e.t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge main", "main")
			return []string{c}
		}},
		{"an edit and a merge of a side branch that edits", func(e *env) []string {
			c := commit(e.t, e.checkout, "revise", map[string]string{prdPath: "two\n"})
			side, merge := e.mergeBranch("side", map[string]string{prdPath: "side\n"})
			return []string{c, side, merge}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			from := commit(t, e.checkout, "prd", map[string]string{prdPath: prdBody})
			want := tc.build(e)
			got := e.editCommits(from, e.base)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("edits %v, want %v", got, want)
			}
		})
	}
}
