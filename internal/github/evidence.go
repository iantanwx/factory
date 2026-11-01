package github

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/qa"
)

// evidenceRef is the ref that holds the evidence commit of QA run run on
// ticket. It lies outside refs/heads/, so its push starts no workflow and
// the branch list stays as it is.
func evidenceRef(ticket, run string) string { return "refs/loop/qa/" + ticket + "/" + run }

// evidenceIdent is the author and the committer of each evidence commit.
const evidenceIdent = "loop <loop@users.noreply.github.com>"

// PushEvidence writes files into one commit with no parent in the mirror,
// and pushes it as the loop App to evidenceRef(ticket, run). The ref
// belongs to the one run, so a report sent again after a failure replaces
// the commit there. It answers "" and pushes nothing for no files.
func (s *Service) PushEvidence(ctx context.Context, p config.Project, ticket, run string, files map[string][]byte) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return "", err
	}
	tree, err := m.writeTree(ctx, files)
	if err != nil {
		return "", err
	}
	at := s.cfg.Now().Unix()
	obj := fmt.Sprintf("tree %s\nauthor %s %d +0000\ncommitter %s %d +0000\n\nQA evidence of %s, run %s\n",
		tree, evidenceIdent, at, evidenceIdent, at, ticket, run)
	commit, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"hash-object", "-t", "commit", "-w", "--stdin"}, stdin: []byte(obj)})
	if err != nil {
		return "", err
	}
	tok, err := s.Token(ctx, p, ScopePush)
	if err != nil {
		return "", err
	}
	ref := evidenceRef(ticket, run)
	out, err := m.git.run(ctx, gitRun{dir: m.Dir, url: m.URL, token: tok, args: []string{
		"push", "--quiet", "--porcelain", "--no-follow-tags", "--no-recurse-submodules", "--no-verify",
		m.URL, "+" + commit + ":" + ref,
	}})
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && strings.Contains(string(out)+ge.Stderr, "rejected") {
			return "", &RefusedError{Code: cli.CodeGitHubRefused, Rule: "evidence_push_refused", AllowedNext: []string{verbEscalationRaise},
				Hint: fmt.Sprintf("GitHub refused the push of the evidence to %s; a person must allow the loop App to push it", ref)}
		}
		return "", &UnavailableError{Err: err}
	}
	if s.crash != nil && s.crash("evidence_pushed") {
		return "", errCrash
	}
	return commit, nil
}

// writeTree writes files into the mirror's objects, and returns their
// tree. Each key is a slash path, and each directory is a tree of its own.
func (m *Mirror) writeTree(ctx context.Context, files map[string][]byte) (string, error) {
	blobs, dirs := map[string][]byte{}, map[string]map[string][]byte{}
	for p, data := range files {
		name, rest, nested := strings.Cut(p, "/")
		if !nested {
			blobs[name] = data
			continue
		}
		if dirs[name] == nil {
			dirs[name] = map[string][]byte{}
		}
		dirs[name][rest] = data
	}
	var entries strings.Builder
	for name, data := range blobs {
		if dirs[name] != nil {
			return "", fmt.Errorf("github: the evidence holds %s as a file and as a directory", name)
		}
		sha, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"hash-object", "-w", "--stdin"}, stdin: data})
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&entries, "100644 blob %s\t%s\x00", sha, name)
	}
	for name, sub := range dirs {
		sha, err := m.writeTree(ctx, sub)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&entries, "040000 tree %s\t%s\x00", sha, name)
	}
	return m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"mktree", "-z"}, stdin: []byte(entries.String())})
}

// evidenceLinks maps each evidence path that r cites to its link on
// GitHub, https://github.com/<owner>/<repo>/blob/<commit>/<path>. It is
// empty with no commit.
func evidenceLinks(p config.Project, commit string, r qa.Report) map[string]string {
	links := map[string]string{}
	if commit == "" {
		return links
	}
	for _, c := range r.Checks {
		for _, f := range c.Findings {
			for _, path := range f.Evidence {
				segs := strings.Split(path, "/")
				for i, seg := range segs {
					segs[i] = url.PathEscape(seg)
				}
				links[path] = "https://github.com/" + p.Repo + "/blob/" + commit + "/" + strings.Join(segs, "/")
			}
		}
	}
	return links
}

// secretInEvidence is the SECRET_IN_OUTPUT refusal of the first evidence
// file, by path, that holds a secret's value, or nil.
func (s *Service) secretInEvidence(files map[string][]byte) *cli.Refusal {
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if ref := s.cfg.Redactor.Refusal("secret_in_evidence", "evidence file "+p, string(files[p])); ref != nil {
			return ref
		}
	}
	return nil
}
