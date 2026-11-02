package gates

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/captures"
)

// The pixel fixtures: ticket t-1 of epic factorio-e, on branch loop/t-1.
const (
	pixelEpic   = "factorio-e"
	pixelBranch = "loop/t-1"
	pixelRecord = "docs/design/factorio-e/record.md"
)

// pixelState is one state line of the fixture record. An empty mark is no
// pixel mark.
type pixelState struct{ id, mark string }

// picture is a PNG of w by h black pixels, the first differing of them red.
func picture(t *testing.T, w, h, differing int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range w * h {
		c := color.NRGBA{A: 255}
		if i < differing {
			c.R = 255
		}
		img.SetNRGBA(i%w, i/w, c)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// pixelOrigin makes the origin repository. Its main holds the record of
// states, with a black w by h picture of each. loop/t-1 holds one commit
// past main, its head, and is checked out.
func pixelOrigin(t *testing.T, w, h int, states ...pixelState) (src, head string) {
	t.Helper()
	lines := []string{"# Design record", "", "**Epic:** " + pixelEpic, "**PRD:** `docs/prd/e.md`", "", "### S1. Home", "", "**Stories:** US-01", ""}
	files := map[string]string{}
	for _, s := range states {
		l := "- **" + s.id + " View.** Picture `" + s.id + ".png`, target `" + s.id + "`"
		if s.mark != "" {
			l += ", " + s.mark
		}
		lines = append(lines, l+".")
		files["docs/design/"+pixelEpic+"/"+s.id+".png"] = picture(t, w, h, 0)
	}
	files[pixelRecord] = strings.Join(lines, "\n") + "\n"
	r := testRepo(t, files)
	git(t, r.Root, "checkout", "-q", "-b", pixelBranch)
	commit(t, r, "feat: draw the home screen", map[string]string{"home.txt": "home\n"})
	return r.Root, strings.TrimSpace(git(t, r.Root, "rev-parse", "HEAD"))
}

// pushCaptures puts the manifest that cites states and files on the
// capture branch of src, in one parentless commit. A file named
// screens.json replaces the manifest.
func pushCaptures(t *testing.T, src string, states []string, files map[string]string) {
	t.Helper()
	raw, err := json.Marshal(captures.ManifestOf("t-1", pixelEpic, states))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, map[string]string{captures.ManifestFile: string(raw)})
	write(t, dir, files)
	git(t, dir, "add", "-A")
	git(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "captures")
	git(t, dir, "push", "-q", src, "HEAD:refs/heads/"+captures.Branch("t-1"))
}

// pixelClone clones src and checks out loop/t-1, as a run outside CI.
func pixelClone(t *testing.T, src string) *Repo {
	t.Helper()
	t.Setenv("GITHUB_EVENT_PATH", "")
	r := cloneRepo(t, src)
	git(t, r.Root, "checkout", "-q", pixelBranch)
	return r
}

// event writes a GitHub event file and names it in GITHUB_EVENT_PATH.
func event(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "event.json")
	write(t, filepath.Dir(p), map[string]string{"event.json": body})
	t.Setenv("GITHUB_EVENT_PATH", p)
}

func runPixel(t *testing.T, r *Repo) []Finding {
	t.Helper()
	fs, err := Pixel(context.Background(), r)
	if err != nil {
		t.Fatalf("Pixel: %v", err)
	}
	return fs
}

func TestC21PixelOverItsThresholdFailsTheGate(t *testing.T) {
	src, head := pixelOrigin(t, 200, 100, pixelState{"S1.1", "pixel 1%"})
	pushCaptures(t, src, []string{"S1.1"}, map[string]string{head + "/S1.1.png": picture(t, 200, 100, 500)})
	got := runPixel(t, pixelClone(t, src))
	want := []Finding{{File: pixelRecord, Line: 10, Rule: "pixel_over", Message: "S1.1 differs from its picture in 2.50% of its pixels, over its bound of 1%"}}
	if !slices.Equal(got, want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
}

func TestC22PixelBoundIsExactAtEachEdge(t *testing.T) {
	for _, tc := range []struct {
		mark      string
		w, h      int
		differing int
		want      string // the pixel_over message, or "" for a pass
	}{
		{"pixel", 200, 100, 100, ""},
		{"pixel", 200, 100, 101, "S1.1 differs from its picture in 0.51% of its pixels, over its bound of 0.5%"},
		{"pixel 0%", 200, 100, 0, ""},
		{"pixel 0%", 200, 100, 1, "S1.1 differs from its picture in 0.01% of its pixels, over its bound of 0%"},
		{"pixel 5%", 200, 100, 1000, ""},
		{"pixel 5%", 200, 100, 1001, "S1.1 differs from its picture in 5.01% of its pixels, over its bound of 5%"},
		{"pixel 0.125%", 200, 40, 10, ""},
		{"pixel 0.125%", 200, 40, 11, "S1.1 differs from its picture in 0.14% of its pixels, over its bound of 0.125%"},
	} {
		src, head := pixelOrigin(t, tc.w, tc.h, pixelState{"S1.1", tc.mark})
		pushCaptures(t, src, []string{"S1.1"}, map[string]string{head + "/S1.1.png": picture(t, tc.w, tc.h, tc.differing)})
		var got []string
		for _, f := range runPixel(t, pixelClone(t, src)) {
			got = append(got, f.Rule+": "+f.Message)
		}
		var want []string
		if tc.want != "" {
			want = []string{"pixel_over: " + tc.want}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s at %d of %d: %q, want %q", tc.mark, tc.differing, tc.w*tc.h, got, want)
		}
	}
}

func TestC24PixelMarkedStateWithNoCaptureAtTheHeadFails(t *testing.T) {
	src, head := pixelOrigin(t, 20, 10, pixelState{"S1.1", "pixel"})
	older := strings.TrimSpace(git(t, src, "rev-parse", "HEAD~1"))
	pushCaptures(t, src, []string{"S1.1"}, map[string]string{older + "/S1.1.png": picture(t, 20, 10, 0)})
	got := runPixel(t, pixelClone(t, src))
	if !slices.Equal(rules(got), []string{"pixel_no_capture"}) || !strings.Contains(got[0].Message, "S1.1") || !strings.Contains(got[0].Message, head) {
		t.Fatalf("findings = %v, want pixel_no_capture naming S1.1 and %s", got, head)
	}
}

func TestC25PixelReadsMarkedStatesOnly(t *testing.T) {
	states := []pixelState{{"S1.1", "pixel 1%"}, {"S1.2", ""}}
	for _, tc := range []struct {
		differing int
		want      []string
	}{{200, nil}, {201, []string{"pixel_over"}}} {
		src, head := pixelOrigin(t, 200, 100, states...)
		pushCaptures(t, src, []string{"S1.1", "S1.2"}, map[string]string{
			head + "/S1.1.png": picture(t, 200, 100, tc.differing),
			head + "/S1.2.png": "no decoder reads these bytes",
		})
		got := runPixel(t, pixelClone(t, src))
		if !slices.Equal(rules(got), tc.want) || len(got) > 0 && !strings.HasPrefix(got[0].Message, "S1.1 ") {
			t.Errorf("S1.1 at %d differing: %v, want %v for S1.1 only", tc.differing, got, tc.want)
		}
	}
}

func TestC25PixelPassesATicketThatCitesNoMarkedState(t *testing.T) {
	src, head := pixelOrigin(t, 20, 10, pixelState{"S1.2", ""})
	pushCaptures(t, src, []string{"S1.2"}, map[string]string{head + "/S1.2.png": "no decoder reads these bytes"})
	if got := runPixel(t, pixelClone(t, src)); len(got) != 0 {
		t.Fatalf("findings = %v, want none", got)
	}
}

func TestC25PixelPassesABranchWithNoCaptureBranch(t *testing.T) {
	src, _ := pixelOrigin(t, 20, 10, pixelState{"S1.1", "pixel"})
	if got := runPixel(t, pixelClone(t, src)); len(got) != 0 {
		t.Fatalf("findings = %v, want none", got)
	}
}

func TestC25PixelPicturesOfTwoSizesFail(t *testing.T) {
	src, head := pixelOrigin(t, 200, 100, pixelState{"S1.1", "pixel 5%"})
	pushCaptures(t, src, []string{"S1.1"}, map[string]string{head + "/S1.1.png": picture(t, 200, 101, 0)})
	got := runPixel(t, pixelClone(t, src))
	if !slices.Equal(rules(got), []string{"pixel_size"}) || !strings.Contains(got[0].Message, "200x101") || !strings.Contains(got[0].Message, "200x100") {
		t.Fatalf("findings = %v, want pixel_size naming 200x101 and 200x100", got)
	}
}

func TestPixelManifestFaults(t *testing.T) {
	src, head := pixelOrigin(t, 20, 10, pixelState{"S1.1", "pixel"})
	pushCaptures(t, src, []string{"S1.1", "S9.9"}, map[string]string{head + "/S1.1.png": picture(t, 20, 10, 0), head + "/S9.9.png": picture(t, 20, 10, 0)})
	got := runPixel(t, pixelClone(t, src))
	if !slices.Equal(rules(got), []string{"pixel_state_unknown"}) || !strings.Contains(got[0].Message, "S9.9") {
		t.Fatalf("an unknown state: %v, want pixel_state_unknown naming S9.9", got)
	}

	src, head = pixelOrigin(t, 20, 10, pixelState{"S1.1", "pixel"})
	pushCaptures(t, src, []string{"S1.1"}, map[string]string{head + "/S1.1.png": picture(t, 20, 10, 0), captures.ManifestFile: "{"})
	if got := runPixel(t, pixelClone(t, src)); !slices.Equal(rules(got), []string{"pixel_manifest"}) {
		t.Fatalf("a manifest that does not parse: %v, want pixel_manifest", got)
	}
}

func TestC26PixelReadsThePullRequestHeadNotTheMergeCommit(t *testing.T) {
	src, head := pixelOrigin(t, 200, 100, pixelState{"S1.1", "pixel 1%"})
	pushCaptures(t, src, []string{"S1.1"}, map[string]string{head + "/S1.1.png": picture(t, 200, 100, 500)})
	r := pixelClone(t, src)
	// GitHub's merge commit of a pull request: the base first, then the head.
	git(t, r.Root, "checkout", "-q", "--detach", "origin/main")
	git(t, r.Root, "merge", "-q", "--no-ff", "-m", "Merge "+head+" into main", head)
	event(t, `{"pull_request": {"head": {"sha": "`+head+`", "ref": "`+pixelBranch+`"}}}`)
	if got := runPixel(t, r); !slices.Equal(rules(got), []string{"pixel_over"}) {
		t.Fatalf("findings = %v, want the pixel_over of the capture at the head %s", got, head)
	}
}

// A remote that no fetch can reach: each fetch fails the gate with
// pixel_fetch, so a pass with no finding ran no fetch.
func unreachable(t *testing.T, r *Repo) {
	git(t, r.Root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
}

func TestC26PixelRunsNoFetchWithNoPullRequest(t *testing.T) {
	src, head := pixelOrigin(t, 20, 10, pixelState{"S1.1", "pixel"})
	for name, body := range map[string]string{
		"push":        `{"ref": "refs/heads/main", "after": "` + head + `"}`,
		"merge_group": `{"merge_group": {"head_sha": "` + head + `", "head_ref": "refs/heads/gh-readonly-queue/main/pr-1"}}`,
	} {
		r := pixelClone(t, src)
		unreachable(t, r)
		event(t, body)
		if got := runPixel(t, r); len(got) != 0 {
			t.Errorf("a %s event: %v, want no finding and no fetch", name, got)
		}
	}
	r := pixelClone(t, src)
	unreachable(t, r)
	git(t, r.Root, "checkout", "-q", "--detach", "HEAD")
	if got := runPixel(t, r); len(got) != 0 {
		t.Errorf("a detached HEAD: %v, want no finding and no fetch", got)
	}
}

func TestC26PixelFetchThatFailsNamesTheBranch(t *testing.T) {
	src, _ := pixelOrigin(t, 20, 10, pixelState{"S1.1", "pixel"})
	r := pixelClone(t, src)
	unreachable(t, r)
	got := runPixel(t, r)
	if !slices.Equal(rules(got), []string{"pixel_fetch"}) || !strings.Contains(got[0].Message, "loop/t-1-captures") {
		t.Fatalf("findings = %v, want pixel_fetch naming loop/t-1-captures", got)
	}
}

// The direct mechanism merges the base into a reviewed head, and the new
// head holds no captures. The gate then reads the captures of its first
// parent, when that parent holds a complete set and the second parent is
// on the base (decision factorio-xi1p). Each case breaks one condition.
func TestPixelReadsTheFirstParentOfABaseMerge(t *testing.T) {
	for _, tc := range []struct {
		name string
		// make moves loop/t-1 in src past its captured head.
		make func(t *testing.T, src string)
		// cited are the states that the manifest cites. The captured head
		// holds S1.1 only.
		cited []string
		want  string
	}{
		{"every condition holds", mergeBase, []string{"S1.1"}, "pixel_over"},
		{"the head is no merge commit", func(t *testing.T, src string) {
			git(t, src, "commit", "-q", "--allow-empty", "-m", "feat: draw more")
		}, []string{"S1.1"}, "pixel_no_capture"},
		{"the first parent holds no complete set", mergeBase, []string{"S1.1", "S1.2"}, "pixel_no_capture"},
		{"the second parent is not on the base", func(t *testing.T, src string) {
			git(t, src, "checkout", "-q", "-b", "side", "main")
			git(t, src, "commit", "-q", "--allow-empty", "-m", "feat: side")
			git(t, src, "checkout", "-q", pixelBranch)
			git(t, src, "merge", "-q", "--no-ff", "-m", "Merge side", "side")
		}, []string{"S1.1"}, "pixel_no_capture"},
	} {
		src, head := pixelOrigin(t, 200, 100, pixelState{"S1.1", "pixel 1%"}, pixelState{"S1.2", ""})
		pushCaptures(t, src, tc.cited, map[string]string{head + "/S1.1.png": picture(t, 200, 100, 500)})
		tc.make(t, src)
		if got := runPixel(t, pixelClone(t, src)); !slices.Equal(rules(got), []string{tc.want}) {
			t.Errorf("%s: %v, want %s", tc.name, got, tc.want)
		}
	}
}

// mergeBase merges a new commit of main into loop/t-1, as the direct
// mechanism's MergeBase does: the head first, then the base.
func mergeBase(t *testing.T, src string) {
	git(t, src, "checkout", "-q", "main")
	git(t, src, "commit", "-q", "--allow-empty", "-m", "feat: change the base")
	git(t, src, "checkout", "-q", pixelBranch)
	git(t, src, "merge", "-q", "--no-ff", "-m", "Merge main into "+pixelBranch, "main")
}

func TestPixelGateIsSlow(t *testing.T) {
	all, _ := Select(nil, false, false, "")
	fast, _ := Select(nil, false, true, "")
	has := func(gs []Gate) bool { return slices.ContainsFunc(gs, func(g Gate) bool { return g.Name == "pixel" }) }
	if !has(all) || has(fast) {
		t.Fatalf("pixel in every gate: %v, in --fast: %v; want true and false", has(all), has(fast))
	}
}
