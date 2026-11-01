package github

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/secrets"
)

// gitlinkMode is the tree mode of a submodule commit.
const gitlinkMode = "160000"

// zeroSHA is the object id git prints for a side that does not exist.
const zeroSHA = "0000000000000000000000000000000000000000"

// MergeConfigPaths are the places Mergify reads its configuration from,
// relative to the repository root. The queue's conditions live there, so
// an edit can weaken one, and a broken config leaves every PR queued with
// no outcome. No item branch changes one: the operator does, by hand.
var MergeConfigPaths = []string{".mergify.yml", ".mergify/config.yml", ".github/mergify.yml"}

// skillRoots are the directories that hold the skills the server injects
// into a checkout: the pack's loop- skills and the project's pins.
var skillRoots = []string{".claude/skills/", ".agents/skills/"}

// injected reports whether p is a path the server injects (KTD17, KTD19):
// one of cli.InjectedFiles, a file of the pack, or a file of one of pins,
// the project's userspace skills (ADR 0013). A pin matches its own
// directory only, so neon covers .claude/skills/neon/ and not
// .claude/skills/neon-postgres/. A commit that touches one would publish
// the brief, the run token, a skill or the .env file. Every other .loop/
// path, such as .loop/policy/<role>.md, .loop/config.yaml or
// .loop/secrets.yaml, belongs to the repository, and a run may change it.
func injected(p string, pins []string) bool {
	if slices.Contains(cli.InjectedFiles(), p) {
		return true
	}
	for _, root := range skillRoots {
		rest, ok := strings.CutPrefix(p, root)
		if !ok {
			continue
		}
		if strings.HasPrefix(rest, "loop-") {
			return true
		}
		if name, _, ok := strings.Cut(rest, "/"); ok && slices.Contains(pins, name) {
			return true
		}
	}
	return false
}

// change is one entry of `git diff-tree --raw`.
type change struct {
	oldMode, newMode string
	oldSHA, newSHA   string
	status           string
	path             string
}

// Scan checks every commit that head adds over base and the other excluded
// refs (KTD14).
// It reads the tree change and the message of each commit, not the net
// diff, so a secret that one commit adds and a later commit removes is
// still found. The first fault refuses the push:
//
//   - a change to .gitmodules, a gitlink, a new filter or LFS line in a
//     .gitattributes file, or a write or a delete of a merge queue config
//     (MergeConfigPaths) is UNSAFE_PATH: the operator makes it by hand. A
//     commit whose config matches the base where it forked, such as a merge
//     of the base, changes no config: it carries the operator's change;
//   - a change to an injected path, or content that holds a stored secret
//     value, is SECRET_IN_DIFF;
//   - a commit message that holds a stored secret value is
//     SECRET_IN_OUTPUT, because the message is outbound text (KTD27);
//   - with settings set, a commit whose .loop/config.yaml differs in a
//     setting from its merge base with base, or does not parse, is
//     SETTINGS_CHANGE: only the coordinator changes a setting.
//
// pins are the names of the project's userspace skills, from its last
// good config and never from head: a branch could otherwise drop a pin
// and add its directory in one push (ADR 0013 decision 1).
func (m *Mirror) Scan(ctx context.Context, red *secrets.Redactor, settings bool, pins []string, head, base string, exclude ...string) ([]string, error) {
	args := []string{"rev-list", "--reverse", head}
	for _, e := range append([]string{base}, exclude...) {
		if e != "" {
			args = append(args, "^"+e)
		}
	}
	out, err := m.git.text(ctx, gitRun{dir: m.Dir, args: args})
	if err != nil {
		return nil, err
	}
	var commits []string
	if out != "" {
		commits = strings.Split(out, "\n")
	}
	for _, c := range commits {
		if err := m.scanCommit(ctx, red, settings, pins, c, base); err != nil {
			return commits, err
		}
	}
	return commits, nil
}

func (m *Mirror) scanCommit(ctx context.Context, red *secrets.Redactor, settings bool, pins []string, commit, base string) error {
	raw, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "commit", commit}})
	if err != nil {
		return err
	}
	_, msg, _ := bytes.Cut(raw, []byte("\n\n"))
	if hit, names := red.Find(string(msg)); hit {
		return &ScanError{Code: cli.CodeSecretInOutput, Rule: "secret_in_message", Commit: commit, Names: names,
			Reason: "the commit message holds the value of " + strings.Join(names, ", ")}
	}
	changes, err := m.changes(ctx, commit)
	if err != nil {
		return err
	}
	var blobs []string
	for _, ch := range changes {
		if err := m.checkPath(ctx, pins, commit, base, ch); err != nil {
			return err
		}
		if settings && ch.path == config.ProjectConfigPath {
			if err := m.checkSettings(ctx, commit, base, ch); err != nil {
				return err
			}
		}
		if ch.status != "D" && ch.newMode != gitlinkMode && ch.newSHA != zeroSHA {
			blobs = append(blobs, ch.newSHA)
		}
	}
	return m.scanBlobs(ctx, red, commit, changes, blobs)
}

// changes lists the tree changes of commit against each parent, or
// against the empty tree for a root commit.
func (m *Mirror) changes(ctx context.Context, commit string) ([]change, error) {
	out, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"diff-tree", "-r", "-m", "--root", "--no-commit-id",
		"--no-renames", "--raw", "-z", commit}})
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	var changes []change
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.TrimPrefix(fields[i], ":")
		parts := strings.Fields(meta)
		if len(parts) != 5 {
			return nil, fmt.Errorf("github: read the changes of %s: unexpected line %q", short(commit), fields[i])
		}
		changes = append(changes, change{oldMode: parts[0], newMode: parts[1], oldSHA: parts[2], newSHA: parts[3],
			status: parts[4][:1], path: fields[i+1]})
	}
	return changes, nil
}

func (m *Mirror) checkPath(ctx context.Context, pins []string, commit, baseRef string, ch change) error {
	unsafe := func(rule, reason string) error {
		return &ScanError{Code: cli.CodeUnsafePath, Rule: rule, Commit: commit, Path: ch.path,
			Reason: reason + "; the operator makes this change by hand"}
	}
	base := path.Base(ch.path)
	switch {
	case ch.newMode == gitlinkMode || ch.oldMode == gitlinkMode:
		return unsafe("gitlink", fmt.Sprintf("%s is a submodule commit (a gitlink)", ch.path))
	case base == ".gitmodules":
		return unsafe("gitmodules", fmt.Sprintf("%s changes", ch.path))
	case slices.Contains(MergeConfigPaths, ch.path):
		// --no-renames shows a rename away as a delete, so it is refused too.
		same, err := m.matchesForkPoint(ctx, commit, baseRef, ch.path)
		if err != nil {
			return err
		}
		if !same {
			return unsafe("merge_config", fmt.Sprintf("%s is the merge queue's config", ch.path))
		}
	case base == ".gitattributes" && ch.status != "D":
		added, err := m.newAttributeLines(ctx, ch)
		if err != nil {
			return err
		}
		if len(added) > 0 {
			return unsafe("gitattributes_filter", fmt.Sprintf("%s adds a filter or LFS line: %q", ch.path, added[0]))
		}
	}
	if injected(ch.path, pins) {
		return &ScanError{Code: cli.CodeSecretInDiff, Rule: "injected_path", Commit: commit, Path: ch.path,
			Reason: fmt.Sprintf("%s is a path the loop injects into the checkout; remove it from the commit", ch.path)}
	}
	return nil
}

// matchesForkPoint reports whether commit holds the same entry at p, or
// the same absence, as its merge base with base. That is the test a PR's
// file list makes: a merge of the base brings in the base's entry, so it
// matches, while a resolution that writes or drops p against the base
// does not. A commit with no merge base with base never matches.
func (m *Mirror) matchesForkPoint(ctx context.Context, commit, base, p string) (bool, error) {
	if base == "" {
		return false, nil
	}
	fork, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"merge-base", commit, base}})
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && fork == "" && ge.Stderr == "" {
			return false, nil // no merge base: exit 1 with no output
		}
		return false, err
	}
	entry := func(rev string) (string, error) {
		return m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"ls-tree", "--full-tree", rev, "--", p}})
	}
	own, err := entry(commit)
	if err != nil {
		return false, err
	}
	theirs, err := entry(fork)
	if err != nil {
		return false, err
	}
	return own == theirs, nil
}

// checkSettings refuses a commit whose project config differs in a setting
// from the config at its merge base with base, as a PR's file list would
// show it. A merge of the base that brings in the coordinator's change
// matches. A commit with no merge base compares with no config.
func (m *Mirror) checkSettings(ctx context.Context, commit, base string, ch change) error {
	refuse := func(rule, reason string) error {
		return &ScanError{Code: cli.CodeSettingsChange, Rule: rule, Commit: commit, Path: ch.path,
			Reason: reason + "; only the coordinator changes settings, with `loop settings set`"}
	}
	fork, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"merge-base", commit, base}})
	if err != nil {
		var ge *GitError
		if !errors.As(err, &ge) || fork != "" || ge.Stderr != "" {
			return err
		}
	}
	var old []byte
	if fork != "" {
		if old, err = m.blobAt(ctx, fork, ch.path); err != nil {
			return err
		}
	}
	var own []byte
	if ch.newSHA != zeroSHA {
		if own, err = m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "blob", ch.newSHA}}); err != nil {
			return err
		}
	}
	changed, err := config.SettingsChanged(old, own)
	if err != nil {
		return refuse("settings_unparsable", fmt.Sprintf("the server cannot show that %s changes no setting: %v", ch.path, err))
	}
	if changed {
		return refuse("settings_change", fmt.Sprintf("%s changes stages, pools or merge against the merge base", ch.path))
	}
	return nil
}

// blobAt reads the file at p in rev, or nil when rev holds no such file.
func (m *Mirror) blobAt(ctx context.Context, rev, p string) ([]byte, error) {
	entry, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"ls-tree", "--full-tree", rev, "--", p}})
	if err != nil || entry == "" {
		return nil, err
	}
	fields := strings.Fields(entry)
	if len(fields) < 3 || fields[1] != "blob" {
		return nil, nil
	}
	return m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "blob", fields[2]}})
}

// newAttributeLines lists the filter or LFS lines of a .gitattributes
// change that its old side lacks.
func (m *Mirror) newAttributeLines(ctx context.Context, ch change) ([]string, error) {
	read := func(sha string) (map[string]bool, []string, error) {
		if sha == zeroSHA {
			return map[string]bool{}, nil, nil
		}
		data, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "blob", sha}})
		if err != nil {
			return nil, nil, err
		}
		set := map[string]bool{}
		var lines []string
		for _, l := range strings.Split(string(data), "\n") {
			l = strings.TrimSpace(l)
			low := strings.ToLower(l)
			if strings.HasPrefix(l, "#") || (!strings.Contains(low, "filter") && !strings.Contains(low, "lfs")) {
				continue
			}
			set[l] = true
			lines = append(lines, l)
		}
		return set, lines, nil
	}
	old, _, err := read(ch.oldSHA)
	if err != nil {
		return nil, err
	}
	_, lines, err := read(ch.newSHA)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, l := range lines {
		if !old[l] {
			added = append(added, l)
		}
	}
	return added, nil
}

// scanBlobs reads each new blob once and refuses one that holds a stored
// secret value.
func (m *Mirror) scanBlobs(ctx context.Context, red *secrets.Redactor, commit string, changes []change, blobs []string) error {
	if len(blobs) == 0 {
		return nil
	}
	pathOf := map[string]string{}
	for _, ch := range changes {
		if _, ok := pathOf[ch.newSHA]; !ok {
			pathOf[ch.newSHA] = ch.path
		}
	}
	out, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "--batch"},
		stdin: []byte(strings.Join(blobs, "\n") + "\n")})
	if err != nil {
		return err
	}
	rd := bufio.NewReader(bytes.NewReader(out))
	for {
		header, err := rd.ReadString('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		parts := strings.Fields(header)
		if len(parts) != 3 {
			return fmt.Errorf("github: read blobs of %s: unexpected header %q", short(commit), header)
		}
		size, err := strconv.Atoi(parts[2])
		if err != nil {
			return fmt.Errorf("github: read blobs of %s: %w", short(commit), err)
		}
		data := make([]byte, size+1) // the content and its trailing newline
		if _, err := io.ReadFull(rd, data); err != nil {
			return fmt.Errorf("github: read blob %s: %w", short(parts[0]), err)
		}
		if hit, names := red.Find(string(data[:size])); hit {
			p := pathOf[parts[0]]
			return &ScanError{Code: cli.CodeSecretInDiff, Rule: "secret_in_content", Commit: commit, Path: p, Names: names,
				Reason: fmt.Sprintf("%s holds the value of %s", p, strings.Join(names, ", "))}
		}
	}
}

// CheckDesignScope refuses a push of epic's design branch at head that
// reaches outside the epic's design directory (C18). A commit that the
// prototype's branch holds and base lacks is SHAPE_REFUSED with rule
// prototype_in_design: the prototype keeps its own branch. A path that
// differs from the merge base with base outside docs/design/<epic>/ is
// SHAPE_REFUSED with rule design_scope. A merge of the base brings in
// the base's paths, so the merge base moves and the push passes. Each
// hint names every commit or path it refuses.
func (m *Mirror) CheckDesignScope(ctx context.Context, epic, head, base, prototype string) error {
	if m.Resolve(ctx, prototype) != "" {
		own, err := m.lines(ctx, "rev-list", head, "^"+base)
		if err != nil {
			return err
		}
		theirs, err := m.lines(ctx, "rev-list", prototype, "^"+base)
		if err != nil {
			return err
		}
		var shared []string
		for _, c := range own {
			if slices.Contains(theirs, c) {
				shared = append(shared, short(c))
			}
		}
		if len(shared) > 0 {
			return &RefusedError{Code: cli.CodeShapeRefused, Rule: "prototype_in_design",
				Hint: fmt.Sprintf("the design branch holds commits of %s that the base lacks: %s; the prototype stays on its own branch, so build the design branch from the base again without them",
					strings.TrimPrefix(prototype, "refs/remotes/origin/"), strings.Join(shared, ", "))}
		}
	}
	fork, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"merge-base", head, base}})
	if err != nil {
		return err
	}
	changed, err := m.lines(ctx, "diff", "--name-only", "--no-renames", fork, head)
	if err != nil {
		return err
	}
	dir := "docs/design/" + epic + "/"
	var outside []string
	for _, p := range changed {
		if !strings.HasPrefix(p, dir) {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 {
		return &RefusedError{Code: cli.CodeShapeRefused, Rule: "design_scope",
			Hint: fmt.Sprintf("the design branch holds only %s; these paths differ from the merge base outside it: %s", dir, strings.Join(outside, ", "))}
	}
	return nil
}

// lines runs git in the mirror and splits its output into lines.
func (m *Mirror) lines(ctx context.Context, args ...string) ([]string, error) {
	out, err := m.git.text(ctx, gitRun{dir: m.Dir, args: args})
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}
