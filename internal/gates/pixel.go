package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/big"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/captures"
	"github.com/iantanwx/loop/internal/design"
	"github.com/iantanwx/loop/internal/pixel"
)

// Pixel compares the capture of each cited state that holds a pixel mark
// with the record's picture of it (factorio-4lso). It reads the captures
// of the pull request's head from <branch>-captures on origin, and the
// cited states from that branch's screens.json. A state with no mark is
// never read. A state fails when the share of its differing pixels is
// over its mark, exactly.
//
// In CI the head and the branch come from the pull request of the event
// at GITHUB_EVENT_PATH, since HEAD is GitHub's merge commit there.
// Outside CI they are HEAD and the current branch. An event with no pull
// request, and a detached HEAD outside CI, read nothing and pass. So does
// a branch with no capture branch on origin: its ticket cites nothing.
func Pixel(ctx context.Context, r *Repo) ([]Finding, error) {
	head, branch, err := pixelHead(ctx, r)
	if err != nil || head == "" {
		return nil, err
	}
	capBranch := branch + "-captures"
	ref := "refs/remotes/origin/" + capBranch
	found, err := fetchCaptures(ctx, r, capBranch)
	if err != nil {
		return []Finding{{Rule: "pixel_fetch", Message: fmt.Sprintf("the fetch of %s from origin failed: %v", capBranch, err)}}, nil
	}
	if !found {
		return nil, nil
	}
	var m captures.Manifest
	raw, err := r.Git(ctx, "show", ref+":"+captures.ManifestFile)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &m)
	}
	if err != nil {
		return []Finding{{Rule: "pixel_manifest", Message: fmt.Sprintf("%s holds no %s that parses: %v", capBranch, captures.ManifestFile, err)}}, nil
	}
	tree, err := r.Git(ctx, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	for _, f := range strings.Split(tree, "\n") {
		files[f] = true
	}
	at := captureHead(ctx, r, head, m.States, files)
	text, _, err := readInRepo(r.Root, m.Record)
	if err != nil {
		return nil, err
	}
	dir := path.Dir(m.Record) + "/"
	rec, _ := design.Parse(dir, text)
	var out []Finding
	add := func(line int, rule, format string, a ...any) {
		out = append(out, Finding{File: m.Record, Line: line, Rule: rule, Message: fmt.Sprintf(format, a...)})
	}
	for _, id := range m.States {
		st, ok := stateOf(rec, id)
		switch {
		case !ok:
			add(0, "pixel_state_unknown", "%s cites %s, and %s holds no such state", captures.ManifestFile, id, m.Record)
			continue
		case st.Pixel == nil:
			continue
		case !files[at+"/"+id+".png"]:
			add(st.Line, "pixel_no_capture", "%s has no capture at %s on %s; capture that head, and push with loop branch push --captures", id, head, capBranch)
			continue
		}
		raw, err := r.Git(ctx, "show", ref+":"+at+"/"+id+".png")
		if err != nil {
			return nil, err
		}
		capture, err := png.Decode(strings.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%s:%s/%s.png: %w", capBranch, at, id, err)
		}
		want, err := decodeFile(r.Root, dir+st.Picture)
		if err != nil {
			return nil, err
		}
		res := pixel.Compare(want, capture)
		bound := strconv.FormatFloat(st.Pixel.Percent, 'f', -1, 64)
		limit, _ := new(big.Rat).SetString(bound)
		switch {
		case res.SizeMismatch:
			add(st.Line, "pixel_size", "%s: the capture is %s and its picture is %s; a crop or a scale would hide a change of layout", id, size(capture), size(want))
		case pixel.Over(res, limit):
			add(st.Line, "pixel_over", "%s differs from its picture in %s of its pixels, over its bound of %s%%", id, shareRoundedUp(res), bound)
		}
	}
	return out, nil
}

func pixelHead(ctx context.Context, r *Repo) (head, branch string, err error) {
	if p := os.Getenv("GITHUB_EVENT_PATH"); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", "", err
		}
		var ev struct {
			PullRequest *struct {
				Head struct {
					SHA string `json:"sha"`
					Ref string `json:"ref"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return "", "", fmt.Errorf("the event at GITHUB_EVENT_PATH: %w", err)
		}
		if ev.PullRequest == nil {
			return "", "", nil
		}
		return ev.PullRequest.Head.SHA, ev.PullRequest.Head.Ref, nil
	}
	ref, err := r.Git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", "", nil
	}
	sha, err := r.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(sha), strings.TrimSpace(ref), nil
}

// fetchCaptures fetches branch from origin. found is false when origin
// holds no such branch. A checkout with a shallow fetch holds no capture
// branch, so the gate fetches it and never trusts the refs it finds.
func fetchCaptures(ctx context.Context, r *Repo, branch string) (found bool, err error) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	c := exec.CommandContext(fctx, "git", "fetch", "--quiet", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch)
	c.Dir = r.Root
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	c.WaitDelay = time.Second
	out, err := c.CombinedOutput()
	switch {
	case err == nil:
		return true, nil
	case strings.Contains(string(out), "couldn't find remote ref"):
		return false, nil
	}
	return false, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
}

// captureHead is the head whose captures the gate reads: head, or the
// first parent of a head that holds no capture, when the direct mechanism
// made that head by a merge of the base (decision factorio-xi1p). That is
// so only when head is a merge commit, its first parent holds a capture
// of each cited state, and its second parent is on the base.
func captureHead(ctx context.Context, r *Repo, head string, states []string, files map[string]bool) string {
	for f := range files {
		if strings.HasPrefix(f, head+"/") {
			return head
		}
	}
	out, err := r.Git(ctx, "rev-list", "--parents", "-n", "1", head)
	parents := strings.Fields(out)
	if err != nil || len(parents) != 3 {
		return head
	}
	first, second := parents[1], parents[2]
	for _, id := range states {
		if !files[first+"/"+id+".png"] {
			return head
		}
	}
	if _, err := r.Git(ctx, "merge-base", "--is-ancestor", second, r.Base); err != nil {
		return head
	}
	_, _ = fmt.Fprintf(r.Out, "pixel: %s merges %s of %s into %s, and holds no capture; reading the captures of %s\n", head, second, r.Base, first, first)
	return first
}

func stateOf(rec design.Record, id string) (design.State, bool) {
	for _, s := range rec.Screens {
		for _, st := range s.States {
			if st.ID == id {
				return st, true
			}
		}
	}
	return design.State{}, false
}

func decodeFile(root, p string) (image.Image, error) {
	raw, ok, err := readInRepo(root, p)
	if err == nil && !ok {
		err = os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	img, err := png.Decode(strings.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return img, nil
}

func size(img image.Image) string {
	b := img.Bounds()
	return fmt.Sprintf("%dx%d", b.Dx(), b.Dy())
}

func shareRoundedUp(r pixel.Result) string {
	h := (int64(r.Differing)*10000 + int64(r.Total) - 1) / int64(r.Total)
	return fmt.Sprintf("%d.%02d%%", h/100, h%100)
}
