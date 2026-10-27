package merge_test

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/direct"
	"github.com/iantanwx/loop/internal/settings"
	"github.com/iantanwx/loop/internal/store"
)

// settingsBranch is the branch of the one change the tests record.
const settingsBranch = "loop/settings/1"

// withSettings composes the hand-off job again, with a settings service
// over the world's store and GitHub.
func (e *env) withSettings() {
	e.t.Helper()
	svc := &settings.Service{Store: e.st, GitHub: e.svc,
		Host: func(context.Context) (*config.HostConfig, error) { return config.DefaultHost(e.t.TempDir()), nil },
		Repo: func(context.Context, string) (settings.Repo, error) {
			return settings.Repo{Dir: e.project.Path, Branch: "main", Head: gt.Git(e.t, e.project.Path, "rev-parse", "HEAD")}, nil
		}}
	job, err := merge.New(merge.Config{GitHub: e.svc, Machine: e.m, Store: e.st, Projects: staticProjects{e.project},
		Source:     func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error) { return e.source(ctx, p) },
		Mechanisms: map[config.MergeMechanism]merge.Mechanism{e.mech.Name(): e.mech}, Settings: svc})
	if err != nil {
		e.t.Fatal(err)
	}
	e.job = job
}

// settingsChange records an open change whose pull request the server
// wrote at head, with the ruleset's ci check green there.
func (e *env) settingsChange(head string) *gt.PR {
	e.t.Helper()
	pr := e.world.Add(&gt.PR{Head: settingsBranch, HeadSHA: head, Title: "chore(settings): set stages.implementer.cap to 3"})
	e.world.SetRuns(head, gt.CheckRun("ci", head, "completed", "success", time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)))
	if err := e.st.Write(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings_changes (project, setting, old_value, old_source, new_value, reason, actor, branch,
			base_sha, pr, head_sha, state, created_at) VALUES (?, 'stages.implementer.cap', '8', 'host', '3', 'fewer runs',
			'coordinator', ?, ?, ?, ?, 'open', ?)`, testProject, settingsBranch, gt.BaseSHA, pr.Number, head, tx.Now().Unix())
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return pr
}

func (e *env) changeState() (state, mergeSHA string) {
	e.t.Helper()
	var m sql.NullString
	if err := e.st.Read(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, merge_sha FROM settings_changes WHERE branch = ?`, settingsBranch).Scan(&state, &m)
	}); err != nil {
		e.t.Fatal(err)
	}
	return state, m.String
}

// C26: each tick of the hand-off job lands the project's settings change
// through the project's mechanism once its checks are green at the head
// the server wrote, with no person's approval: loop/eligible on that
// head, the hand-off, then the merge.
func TestC26SettingsChangeLandsThroughEachMechanism(t *testing.T) {
	for _, d := range drivers() {
		t.Run(d.name, func(t *testing.T) {
			e := newEnv(t, d.mech)
			e.withSettings()
			head := sha('5')
			pr := e.settingsChange(head)
			e.tick()
			if state, _ := e.changeState(); state != "handed" || !d.handedOff(e, pr) {
				t.Fatalf("after the first tick the change is %s, and %s holds it %v", state, d.name, d.handedOff(e, pr))
			}
			if posted := e.world.Posted(); !slices.Contains(posted, head+" "+gt.Eligible+" success") {
				t.Fatalf("posted %v; want loop/eligible on %s", posted, head)
			}
			if d.merge != nil {
				d.merge(e, pr)
			}
			e.tick()
			if state, m := e.changeState(); state != "merged" || m != gt.MergeSHA(pr.Number) {
				t.Fatalf("after the merge the change is %s at %q; want merged at %s", state, m, gt.MergeSHA(pr.Number))
			}
		})
	}
}

// C30: red CI on the change's head fails it and closes its pull request
// on GitHub.
func TestC30RedSettingsChangeClosesItsPullRequest(t *testing.T) {
	e := newEnv(t, func(e *env) merge.Mechanism { return direct.New(e.svc) })
	e.withSettings()
	pr := e.settingsChange(sha('5'))
	e.world.SetRuns(pr.HeadSHA, failing(pr.HeadSHA))
	e.tick()
	if state, _ := e.changeState(); state != "failed" {
		t.Fatalf("change is %s; want failed", state)
	}
	if got := e.world.PR(pr.Number); got.State != "closed" || got.Merged {
		t.Fatalf("pull request = %+v; want closed with no merge", got)
	}
	if posted := e.world.Posted(); len(posted) != 0 {
		t.Fatalf("posted %v", posted)
	}
}

// The github-queue sweep reads Eligible beside the tickets': it posts
// loop/eligible on the merge group of a handed settings change.
func TestGitHubQueueSweepPostsOnTheGroupOfASettingsChange(t *testing.T) {
	e := newQueue(t)
	e.withSettings()
	pr := e.settingsChange(sha('5'))
	g := sha('a')
	e.world.AddGroup(group(strconv.Itoa(pr.Number), gt.BaseSHA), g)
	e.tick() // the hand-off; the sweep runs after it
	if state, _ := e.changeState(); state != "handed" {
		t.Fatalf("change is %s; want handed", state)
	}
	e.tick()
	if posts(e, g) != 1 {
		t.Fatalf("posted %v; want loop/eligible on group %s", e.world.Posted(), g)
	}
}

// A group of a settings pull request that no handed change holds gets
// nothing.
func TestGitHubQueueSweepSkipsTheGroupOfAnOpenSettingsChange(t *testing.T) {
	e := newQueue(t)
	e.withSettings()
	pr := e.settingsChange(sha('5'))
	e.world.SetRuns(pr.HeadSHA) // no check yet: the change stays open
	g := sha('a')
	e.world.AddGroup(group(strconv.Itoa(pr.Number), gt.BaseSHA), g)
	e.tick()
	e.tick()
	if state, _ := e.changeState(); state != "open" || posts(e, g) != 0 {
		t.Fatalf("change is %s, %d posts on the group; want open and none", state, posts(e, g))
	}
}

// C27: a handed settings change whose head a person moved gets nothing on
// its merge group. A tick whose withdrawal fails leaves the change handed,
// and its sweep still posts nothing. The next tick takes it from the
// queue, fails it and closes its pull request.
func TestGitHubQueueSweepSkipsTheGroupOfAMovedSettingsChange(t *testing.T) {
	e := newQueue(t)
	e.withSettings()
	pr := e.settingsChange(sha('5'))
	e.tick()
	if state, _ := e.changeState(); state != "handed" {
		t.Fatalf("change is %s; want handed", state)
	}
	moved := sha('6')
	e.world.Update(pr, func(p *gt.PR) { p.HeadSHA = moved })
	e.world.SetRuns(moved, gt.CheckRun("ci", moved, "completed", "success", time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)))
	g := sha('a')
	e.world.AddGroup(group(strconv.Itoa(pr.Number), gt.BaseSHA), g)
	e.world.FailNext("POST /graphql", http.StatusUnprocessableEntity)
	if err := e.job.TickProject(e.ctx, testProject); err == nil {
		t.Fatal("a tick whose withdrawal failed answered no error")
	}
	if state, _ := e.changeState(); state != "handed" || posts(e, g) != 0 {
		t.Fatalf("change is %s, posts on the group %d; want handed and none", state, posts(e, g))
	}
	e.tick()
	if state, _ := e.changeState(); state != "failed" || posts(e, g) != 0 || posts(e, moved) != 0 {
		t.Fatalf("change is %s, posts on the group %d, on the moved head %d; want failed and none", state, posts(e, g), posts(e, moved))
	}
	if got := e.world.PR(pr.Number); got.State != "closed" || got.Merged || slices.Contains(e.world.Queue(), pr.Number) {
		t.Fatalf("pull request = %+v, queue %v; want closed and out of the queue", got, e.world.Queue())
	}
}
