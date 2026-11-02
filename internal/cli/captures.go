package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// capturesIdent is the author and the committer of each capture commit.
// The commit holds no time, so the same files at the same head make the
// same commit, and a repeated push derives the same key.
const capturesIdent = "loop <loop@users.noreply.github.com> 0 +0000"

// capturesRef keeps the capture commit of head in the checkout, so the
// server fetches it as a ref tip.
func capturesRef(head string) string { return "refs/loop/captures/" + head }

// CaptureCommit writes each file of dir into the checkout's objects as
// one commit with no parent, keeps it at refs/loop/captures/<head>, and
// returns its sha. dir holds only files. The server checks their names
// and their bytes.
func CaptureCommit(ctx context.Context, checkout, dir, head string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read --captures: %w", err)
	}
	var tree strings.Builder
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return "", fmt.Errorf("--captures %s holds %s, which is not a file", dir, e.Name())
		}
		blob, err := checkoutGit(ctx, checkout, nil, "hash-object", "-w", "--", filepath.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&tree, "100644 blob %s\t%s\x00", blob, e.Name())
	}
	sha, err := checkoutGit(ctx, checkout, []byte(tree.String()), "mktree", "-z")
	if err != nil {
		return "", err
	}
	obj := fmt.Sprintf("tree %s\nauthor %s\ncommitter %s\n\nCaptures of %s\n", sha, capturesIdent, capturesIdent, head)
	if sha, err = checkoutGit(ctx, checkout, []byte(obj), "hash-object", "-t", "commit", "-w", "--stdin"); err != nil {
		return "", err
	}
	if _, err := checkoutGit(ctx, checkout, nil, "update-ref", capturesRef(head), sha); err != nil {
		return "", err
	}
	return sha, nil
}

func checkoutGit(ctx context.Context, checkout string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", checkout}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("write the capture commit: git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// capturesOf reads the HEAD that a push sends, and the capture commit of
// its --captures directory, or "" with no --captures.
func capturesOf(ctx context.Context, inv Invocation, checkout, cwd string) (head, captures string, err error) {
	if head, err = CheckoutHead(ctx, headDir(inv, checkout, cwd)); err != nil {
		return "", "", err
	}
	dir := inv.Flags["captures"]
	if dir == "" || inv.Verb.Path != "branch push" {
		return head, "", nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	captures, err = CaptureCommit(ctx, checkout, dir, head)
	return head, captures, err
}

// PushContentHash is the content hash of a `branch push` of head with the
// capture commit captures, or of head alone when captures is "".
func PushContentHash(head, captures string) string {
	if captures == "" {
		return HeadContentHash(head)
	}
	return hashText("head", head+"\x00"+captures)
}
