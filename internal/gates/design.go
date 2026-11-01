package gates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/design"
	docshape "github.com/iantanwx/loop/internal/shape"
)

// Design checks each design record, docs/design/<epic>/record.md. The
// record parses. It maps each UI: yes story of the PRD that its **PRD:**
// line names to a screen, and each screen cites a story of that PRD. Each
// state's picture is in the record's directory. A repository with no
// record passes.
//
// The pin is checked only for a record that the branch adds or changes
// against the merge base with Base: the remote prototype branch holds the
// pinned commit. A person can delete a prototype branch by hand, and a
// record on the base must not then fail every later build.
func Design(ctx context.Context, r *Repo) ([]Finding, error) {
	files, err := r.glob([]string{"docs/design/*/" + design.RecordFile})
	if err != nil || len(files) == 0 {
		return nil, err
	}
	base := ""
	if mb, err := r.Git(ctx, "merge-base", "HEAD", r.Base); err == nil {
		base = strings.TrimSpace(mb)
	}
	var out []Finding
	for _, f := range files {
		f = filepath.ToSlash(f)
		raw, err := os.ReadFile(filepath.Join(r.Root, f))
		if err != nil {
			return nil, err
		}
		text := string(raw)
		dir := path.Dir(f) + "/"
		rec, problems := design.Parse(dir, text)
		add := func(line int, rule, format string, a ...any) {
			out = append(out, Finding{File: f, Line: line, Rule: rule, Message: fmt.Sprintf(format, a...)})
		}
		for _, p := range problems {
			add(p.Line, p.Rule, "%s", p.Msg)
		}
		if rec.PRD != "" {
			prd, ok, err := readInRepo(r.Root, rec.PRD)
			if err != nil {
				return nil, err
			}
			if !ok {
				add(0, "prd_missing", "the **PRD:** line names %s, and the repository holds no such PRD", rec.PRD)
			} else {
				stories := docshape.Stories(prd)
				var cited []string
				for _, s := range rec.Screens {
					cited = append(cited, s.Stories...)
					for _, l := range s.Stories {
						if !slices.ContainsFunc(stories, func(st docshape.Story) bool { return st.Label == l }) {
							add(s.StoriesLine, "screen_unknown_story", "%s cites %s, and %s holds no such story", s.ID, l, rec.PRD)
						}
					}
				}
				for _, st := range stories {
					if st.UI == docshape.UIYes && !slices.Contains(cited, st.Label) {
						add(0, "ui_story_no_screen", "%s of %s reads UI: yes, and no screen cites it", st.Label, rec.PRD)
					}
				}
			}
		}
		for _, s := range rec.Screens {
			for _, st := range s.States {
				if _, err := os.Stat(filepath.Join(r.Root, dir, st.Picture)); errors.Is(err, os.ErrNotExist) {
					add(st.Line, "picture_missing", "%s names picture %s, and %s holds no such file", st.ID, st.Picture, dir)
				}
			}
		}
		if rec.Pin.Commit == "" {
			continue
		}
		if base != "" {
			if old, err := r.Git(ctx, "show", base+":"+f); err == nil && old == text {
				continue
			}
		}
		ref := "refs/remotes/origin/" + rec.Pin.Branch
		if _, err := r.Git(ctx, "merge-base", "--is-ancestor", rec.Pin.Commit, ref); err != nil {
			add(0, "pin_unreachable", "%s does not hold the pinned commit %s; push the commit to %s, and fetch that branch", ref, short(rec.Pin.Commit), rec.Pin.Branch)
		}
	}
	return out, nil
}

// readInRepo reads the file at p, a path in the repository. ok is false
// when the repository holds no such file.
func readInRepo(root, p string) (text string, ok bool, err error) {
	if !filepath.IsLocal(p) {
		return "", false, nil
	}
	raw, err := os.ReadFile(filepath.Join(root, p))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return string(raw), err == nil, err
}
