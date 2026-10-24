package github

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
)

// gitlinkMode is the tree mode of a submodule commit.
const gitlinkMode = "160000"

// zeroSHA is the object id git prints for a side that does not exist.
const zeroSHA = "0000000000000000000000000000000000000000"

// injectedPrefixes are the pack skill directories the server injects into
// a checkout (KTD17).
var injectedPrefixes = []string{".claude/skills/loop-", ".agents/skills/loop-"}

// injected reports whether p is a path the server injects (KTD17, KTD19):
// one of cli.InjectedFiles, or a file of the pack. A commit that touches
// one would publish the brief, the run token, the pack or the .env file.
// Every other .loop/ path, such as .loop/policy/<role>.md, .loop/config.yaml
// or .loop/secrets.yaml, belongs to the repository, and a run may change it.
func injected(p string) bool {
	if slices.Contains(cli.InjectedFiles(), p) {
		return true
	}
	for _, pre := range injectedPrefixes {
		if strings.HasPrefix(p, pre) {
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

// Scan checks every commit that head adds over the excluded refs (KTD14).
// It reads the tree change and the message of each commit, not the net
// diff, so a secret that one commit adds and a later commit removes is
// still found. The first fault refuses the push:
//
//   - a change to .gitmodules, a gitlink, or a new filter or LFS line in a
//     .gitattributes file is UNSAFE_PATH: the operator makes it by hand;
//   - a change to an injected path, or content that holds a stored secret
//     value, is SECRET_IN_DIFF;
//   - a commit message that holds a stored secret value is
//     SECRET_IN_OUTPUT, because the message is outbound text (KTD27).
func (m *Mirror) Scan(ctx context.Context, red *secrets.Redactor, head string, exclude ...string) ([]string, error) {
	args := []string{"rev-list", "--reverse", head}
	for _, e := range exclude {
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
		if err := m.scanCommit(ctx, red, c); err != nil {
			return commits, err
		}
	}
	return commits, nil
}

func (m *Mirror) scanCommit(ctx context.Context, red *secrets.Redactor, commit string) error {
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
		if err := m.checkPath(ctx, commit, ch); err != nil {
			return err
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

func (m *Mirror) checkPath(ctx context.Context, commit string, ch change) error {
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
	case base == ".gitattributes" && ch.status != "D":
		added, err := m.newAttributeLines(ctx, ch)
		if err != nil {
			return err
		}
		if len(added) > 0 {
			return unsafe("gitattributes_filter", fmt.Sprintf("%s adds a filter or LFS line: %q", ch.path, added[0]))
		}
	}
	if injected(ch.path) {
		return &ScanError{Code: cli.CodeSecretInDiff, Rule: "injected_path", Commit: commit, Path: ch.path,
			Reason: fmt.Sprintf("%s is a path the loop injects into the checkout; remove it from the commit", ch.path)}
	}
	return nil
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
