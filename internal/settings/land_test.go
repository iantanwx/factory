package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/fake"
	"github.com/iantanwx/loop/internal/store"
)

// fakeGitHub is the GitHub side of Land: the pull requests the fixture
// opened, the checks on each head, and every write Land makes.
type fakeGitHub struct {
	prs      map[int]*github.PR
	required []string                     // github.RequiredChecks at the default branch
	runs     map[string][]github.CheckRun // by head
	posted   []string                     // "<sha> <context>"
	closed   []int
}

func (g *fakeGitHub) GetPR(_ context.Context, _ config.Project, n int) (github.PR, error) {
	pr, ok := g.prs[n]
	if !ok {
		return github.PR{}, fmt.Errorf("no PR #%d", n)
	}
	return *pr, nil
}

func (g *fakeGitHub) RequiredChecks(context.Context, config.Project) ([]string, error) {
	return slices.Clone(g.required), nil
}

func (g *fakeGitHub) Checks(_ context.Context, _ config.Project, head string) (github.CIReport, error) {
	return github.Classify(head, g.runs[head], nil), nil
}

func (g *fakeGitHub) PostStatus(_ context.Context, _ config.Project, sha, context, _, _ string) error {
	g.posted = append(g.posted, sha+" "+context)
	return nil
}

func (g *fakeGitHub) ClosePR(_ context.Context, _ config.Project, n int) error {
	g.closed = append(g.closed, n)
	g.prs[n].State = "closed"
	return nil
}

// check reports name on head with conclusion, or still running when
// conclusion is "".
func (g *fakeGitHub) check(head, name, conclusion string) {
	r := github.CheckRun{ID: int64(len(g.runs[head]) + 1), Name: name, HeadSHA: head, Status: "completed", Conclusion: conclusion}
	if conclusion == "" {
		r.Status = "in_progress"
	}
	g.runs[head] = append(g.runs[head], r)
}

// landFixture is a setFixture whose changes Land moves, against a fake
// GitHub, the fake merge mechanism and a clock the test sets.
type landFixture struct {
	*setFixture
	gh      *fakeGitHub
	mech    *fake.Mechanism
	project config.Project
	now     time.Time
	forgot  []string
	// crash makes the next proposal open its pull request and then fail,
	// as a server that stops before it records the move to open.
	crash bool
}

func newLandFixture(t *testing.T, cfg string) *landFixture {
	t.Helper()
	f := &landFixture{setFixture: newSetFixture(t, cfg), mech: fake.New(), project: config.Project{ID: "alpha", Repo: "acme/widgets"},
		gh: &fakeGitHub{prs: map[int]*github.PR{}, required: []string{github.GatesCheck}, runs: map[string][]github.CheckRun{}}}
	f.now = time.Now()
	f.svc.GitHub = f.gh
	f.svc.Now = func() time.Time { return f.now }
	f.svc.Forget = func(project string) { f.forgot = append(f.forgot, project) }
	propose := f.svc.Propose
	// ProposeFile answers the pull request already open on the branch, so
	// a retry after a crash opens no second one.
	f.svc.Propose = func(ctx context.Context, project string, fc github.FileChange) (int, string, error) {
		for n, pr := range f.gh.prs {
			if pr.Head.Ref == fc.Branch && pr.State == "open" {
				f.proposed = append(f.proposed, fc)
				return n, pr.Head.SHA, nil
			}
		}
		n, head, err := propose(ctx, project, fc)
		if err == nil || f.crash {
			if f.crash {
				n, head = 40+len(f.proposed), strings.Repeat("c", 40)
			}
			pr := &github.PR{Number: n, State: "open"}
			pr.Head.Ref, pr.Head.SHA, pr.Base.Ref = fc.Branch, head, "main"
			f.gh.prs[n] = pr
		}
		if f.crash {
			f.crash = false
			return 0, "", errors.New("the server stopped")
		}
		return n, head, err
	}
	return f
}

func (f *landFixture) land() {
	f.t.Helper()
	if err := f.svc.Land(context.Background(), f.project, f.mech); err != nil {
		f.t.Fatalf("land: %v", err)
	}
}

// open records a change of key to value and opens its pull request, whose
// required check passed unless gates is "".
func (f *landFixture) open(key string, value *string, gates string) Change {
	f.t.Helper()
	c := f.accept(key, value)
	if c.State != Open {
		f.t.Fatalf("change = %+v; want open", c)
	}
	if gates != "" {
		f.gh.check(c.Head, github.GatesCheck, gates)
	}
	return c
}

// row is the stored state of one change.
type row struct {
	state, detail, merge string
	created              time.Time
}

func (f *landFixture) row(id int64) row {
	f.t.Helper()
	var r row
	var created int64
	var mergeSHA sql.NullString
	if err := f.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, detail, merge_sha, created_at FROM settings_changes WHERE id = ?`, id).
			Scan(&r.state, &r.detail, &mergeSHA, &created)
	}); err != nil {
		f.t.Fatal(err)
	}
	r.merge, r.created = mergeSHA.String, time.Unix(created, 0)
	return r
}

func (f *landFixture) wantState(id int64, s State) row {
	f.t.Helper()
	r := f.row(id)
	if r.state != string(s) {
		f.t.Fatalf("change %d is %s (%s); want %s", id, r.state, r.detail, s)
	}
	return r
}

type notice struct{ project, subject, body string }

func (f *landFixture) notices() []notice {
	f.t.Helper()
	var out []notice
	if err := f.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT project, subject, body FROM notices WHERE source = 'settings_change' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n notice
			if err := rows.Scan(&n.project, &n.subject, &n.body); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

type alert struct {
	key, body string
	count     int
}

func (f *landFixture) alerts() []alert {
	f.t.Helper()
	var out []alert
	if err := f.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT dedupe_key, body, count FROM alerts WHERE kind = ? AND state = 'open' ORDER BY id`, AlertChangeFailed)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a alert
			if err := rows.Scan(&a.key, &a.body, &a.count); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// eligible reports Eligible of the change's pull request.
func (f *landFixture) eligible(c Change) bool {
	f.t.Helper()
	ok, err := f.svc.Eligible(context.Background(), f.project, c.PR)
	if err != nil {
		f.t.Fatal(err)
	}
	return ok
}

// merge makes the fake mechanism report the change merged, and commits the
// proposed file on main as the merge does.
func (f *landFixture) merge(c Change) string {
	f.t.Helper()
	sha := f.commit(string(f.proposed[len(f.proposed)-1].Content), "mergify[bot]")
	f.mech.Set(c.PR, merge.Result{Outcome: merge.Merged, SHA: sha})
	return sha
}

// C26: an open change whose every required check is green at its recorded
// head gets loop/eligible on that head and goes to the mechanism. A Merged
// poll makes it merged with the merge commit. No step waits on an inbox
// answer, an approval or a run: Land alone moves it.
func TestC26ChangeLandsThroughTheMechanismOnceItsChecksAreGreen(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("stages.implementer.cap", strp("5"), "success")

	f.land()
	f.wantState(c.ID, Handed)
	if want := []string{c.Head + " " + github.EligibleContext}; !slices.Equal(f.gh.posted, want) {
		t.Fatalf("posted %v; want %v", f.gh.posted, want)
	}
	if !f.mech.HandedOff(c.PR) || !f.eligible(c) {
		t.Fatalf("handed off %v, eligible %v; want both", f.mech.HandedOff(c.PR), f.eligible(c))
	}

	f.land() // the mechanism still holds it
	f.wantState(c.ID, Handed)

	sha := f.merge(c)
	f.land()
	if r := f.wantState(c.ID, Merged); r.merge != sha {
		t.Fatalf("merge commit = %q; want %s", r.merge, sha)
	}
	if !slices.Equal(f.forgot, []string{"alpha"}) {
		t.Fatalf("forgot the config of %v; want alpha once", f.forgot)
	}
	if len(f.gh.posted) != 1 || len(f.gh.closed) != 0 || f.eligible(c) {
		t.Fatalf("posted %v, closed %v, eligible %v after the merge", f.gh.posted, f.gh.closed, f.eligible(c))
	}
}

// C27: loop/eligible is posted on a settings pull request only at the head
// the server wrote, and only when every required check is green. A pending
// check posts nothing. A moved head and a red check fail the change, and
// post nothing.
func TestC27EligibleOnlyAtTheWrittenHeadWithEveryCheckGreen(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *landFixture, c Change)
		want  State
	}{
		{"a pending check", func(f *landFixture, c Change) { f.gh.check(c.Head, github.GatesCheck, "") }, Open},
		{"no check reported", func(*landFixture, Change) {}, Open},
		{"a moved head", func(f *landFixture, c Change) {
			f.gh.check(c.Head, github.GatesCheck, "success")
			moved := strings.Repeat("d", 40)
			f.gh.check(moved, github.GatesCheck, "success")
			f.gh.prs[c.PR].Head.SHA = moved
		}, Failed},
		{"a red check", func(f *landFixture, c Change) { f.gh.check(c.Head, github.GatesCheck, "failure") }, Failed},
		{"a red check that is not required", func(f *landFixture, c Change) {
			f.gh.check(c.Head, github.GatesCheck, "success")
			f.gh.check(c.Head, "lint", "failure")
		}, Failed},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLandFixture(t, baseConfig)
			ch := f.open("stages.implementer.cap", strp("5"), "")
			c.setup(f, ch)
			f.land()
			f.wantState(ch.ID, c.want)
			if len(f.gh.posted) != 0 || f.mech.HandedOff(ch.PR) || f.eligible(ch) {
				t.Fatalf("posted %v, handed off %v, eligible %v; want none", f.gh.posted, f.mech.HandedOff(ch.PR), f.eligible(ch))
			}
		})
	}
}

// C27: a handed change whose head a person moved is no longer eligible.
// Land takes it back from the mechanism, fails it, closes its pull request
// and raises one alert. Nothing is posted on the moved head.
func TestC27HandedChangeWhoseHeadMovedFails(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("stages.implementer.cap", strp("5"), "success")
	f.land()
	f.wantState(c.ID, Handed)
	moved := strings.Repeat("d", 40)
	f.gh.check(moved, github.GatesCheck, "success")
	f.gh.prs[c.PR].Head.SHA = moved
	if f.eligible(c) {
		t.Fatal("eligible at a head the server did not write")
	}

	f.land()
	f.wantState(c.ID, Failed)
	if !f.mech.Withdrawn(c.PR) || f.mech.HandedOff(c.PR) {
		t.Fatalf("withdrawn %v, handed off %v; want withdrawn", f.mech.Withdrawn(c.PR), f.mech.HandedOff(c.PR))
	}
	if !slices.Equal(f.gh.closed, []int{c.PR}) {
		t.Fatalf("closed %v; want #%d", f.gh.closed, c.PR)
	}
	if as := f.alerts(); len(as) != 1 || !strings.Contains(as[0].body, "moved") {
		t.Fatalf("alerts = %+v; want one that names the moved head", as)
	}
	if want := []string{c.Head + " " + github.EligibleContext}; !slices.Equal(f.gh.posted, want) || len(f.notices()) != 0 {
		t.Fatalf("posted %v, notices %+v; want only the post before the hand-off", f.gh.posted, f.notices())
	}
}

// C29: each merged change puts exactly one notice. It names the project,
// the setting, the old value with its source, the new value, the merge
// commit and the reason. A tick that reads the same merge again puts no
// second notice.
func TestC29MergedChangePutsExactlyOneNotice(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("stages.implementer.cap", strp("5"), "success")
	f.land()
	sha := f.merge(c)
	f.land()
	f.land()
	// A second read of the same merge: the move to merged and the notice
	// are one write, keyed by the change.
	if err := f.svc.merged(context.Background(), f.project, &landing{Change: c}, sha); err != nil {
		t.Fatal(err)
	}
	ns := f.notices()
	if len(ns) != 1 {
		t.Fatalf("%d notices; want 1: %+v", len(ns), ns)
	}
	n := ns[0]
	if n.project != "alpha" || n.subject != "alpha: stages.implementer.cap 3 (project) to 5 (project)" {
		t.Fatalf("notice = %+v", n)
	}
	for _, want := range []string{"stages.implementer.cap", "3 (project)", "5 (project)", sha, "the reason", fmt.Sprintf("#%d", c.PR)} {
		if !strings.Contains(n.body, want) {
			t.Fatalf("notice body names no %q:\n%s", want, n.body)
		}
	}
}

// C29: an --unset change's notice names the value in force after it and
// that value's source.
func TestC29UnsetNoticeNamesTheValueInForceAfterIt(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("stages.implementer.cap", nil, "success")
	f.land()
	f.merge(c)
	f.land()
	ns := f.notices()
	if len(ns) != 1 || ns[0].subject != "alpha: stages.implementer.cap 3 (project) to 8 (host)" {
		t.Fatalf("notices = %+v; want one that names 8 (host)", ns)
	}
}

// C29: a pull request that a person merged while its change is open makes
// the change merged with the pull request's merge commit, and puts one
// notice. No mechanism reported the merge.
func TestC29ChangeMergedByAPersonPutsOneNotice(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("stages.implementer.cap", strp("5"), "")
	sha := f.commit(string(f.proposed[len(f.proposed)-1].Content), "a person")
	at := f.now
	f.gh.prs[c.PR].MergedAt, f.gh.prs[c.PR].MergeCommitSHA, f.gh.prs[c.PR].State = &at, sha, "closed"

	f.land()
	f.land()
	if r := f.wantState(c.ID, Merged); r.merge != sha {
		t.Fatalf("merge commit = %q; want %s", r.merge, sha)
	}
	ns := f.notices()
	if len(ns) != 1 || !strings.Contains(ns[0].body, sha) || ns[0].subject != "alpha: stages.implementer.cap 3 (project) to 5 (project)" {
		t.Fatalf("notices = %+v; want one that names %s", ns, sha)
	}
	if len(f.alerts()) != 0 || len(f.gh.posted) != 0 || f.mech.HandedOff(c.PR) {
		t.Fatalf("alerts %+v, posted %v, handed off %v", f.alerts(), f.gh.posted, f.mech.HandedOff(c.PR))
	}
}

// C30: a change that cannot land fails, closes its pull request when it is
// open, raises one settings_change_failed alert for the coordinator, and
// puts no notice. After the failure the command accepts a new change.
func TestC30ChangeThatCannotLandFails(t *testing.T) {
	for _, c := range []struct {
		name string
		// cause makes the change fail at the next Land. It returns the
		// change.
		cause func(f *landFixture) Change
		close bool
		want  string // the alert names it
	}{
		{"red CI", func(f *landFixture) Change { return f.open("stages.implementer.cap", strp("5"), "failure") }, true, "gates"},
		{"a conflict", func(f *landFixture) Change {
			c := f.open("stages.implementer.cap", strp("5"), "success")
			// GitHub has not computed the state yet: the change waits.
			f.gh.prs[c.PR].MergeableState = merge.MergeableUnknown
			f.land()
			f.wantState(c.ID, Open)
			if len(f.gh.posted) != 0 || f.mech.HandedOff(c.PR) {
				f.t.Fatalf("posted %v, handed off %v while mergeable is unknown", f.gh.posted, f.mech.HandedOff(c.PR))
			}
			f.gh.prs[c.PR].MergeableState = merge.MergeableDirty
			return c
		}, true, "conflict"},
		{"a dequeue", func(f *landFixture) Change {
			c := f.open("stages.implementer.cap", strp("5"), "success")
			f.land()
			f.mech.Set(c.PR, merge.Result{Outcome: merge.Dequeued, Reason: merge.Red, Detail: "the batch failed"})
			return c
		}, true, "the batch failed"},
		{"a closed poll", func(f *landFixture) Change {
			c := f.open("stages.implementer.cap", strp("5"), "success")
			f.land()
			f.mech.Set(c.PR, merge.Result{Outcome: merge.Closed})
			f.gh.prs[c.PR].State = "closed"
			return c
		}, false, "closed"},
		{"a person closed the pull request", func(f *landFixture) Change {
			c := f.open("stages.implementer.cap", strp("5"), "")
			f.gh.prs[c.PR].State = "closed"
			return c
		}, false, "closed"},
		{"an open change at the limit", func(f *landFixture) Change {
			c := f.open("stages.implementer.cap", strp("5"), "")
			f.now = f.row(c.ID).created.Add(LiveLimit)
			return c
		}, true, "open"},
		{"a proposed change at the limit", func(f *landFixture) Change {
			f.fail = errors.New("github: 502")
			c := f.accept("stages.implementer.cap", strp("5"))
			f.now = f.row(c.ID).created.Add(LiveLimit)
			return c
		}, false, "proposed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLandFixture(t, baseConfig)
			ch := c.cause(f)
			f.land()
			f.wantState(ch.ID, Failed)
			if closed := slices.Contains(f.gh.closed, ch.PR); closed != c.close || len(f.gh.closed) > 1 {
				t.Fatalf("closed %v; want #%d closed %v", f.gh.closed, ch.PR, c.close)
			}
			as := f.alerts()
			if len(as) != 1 || as[0].key != fmt.Sprintf("%s:alpha:%d", AlertChangeFailed, ch.ID) || as[0].count != 1 ||
				!strings.Contains(as[0].body, c.want) {
				t.Fatalf("alerts = %+v; want one for change %d that names %q", as, ch.ID, c.want)
			}
			if ns := f.notices(); len(ns) != 0 {
				t.Fatalf("a failed change put notices %+v", ns)
			}
			f.land() // nothing is live, so nothing moves
			if as := f.alerts(); len(as) != 1 || as[0].count != 1 {
				t.Fatalf("a second tick raised %+v", as)
			}
			f.fail = nil
			if next := f.accept("stages.implementer.cap", strp("6")); next.State != Open {
				t.Fatalf("the change after the failure = %+v; want open", next)
			}
		})
	}
}

// C34: a change that adds audit to merge.required_checks stays open while
// audit has not reported at its head, even with every check in force
// green. When audit never reports, the change fails at LiveLimit, and its
// alert names audit and the state open. At LiveLimit minus one second the
// change is still open.
func TestC34ChangeWaitsForACheckTheProposedListAdds(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("merge.required_checks", strp("gates,audit"), "success")
	f.land()
	f.wantState(c.ID, Open)
	created := f.row(c.ID).created
	f.now = created.Add(LiveLimit - time.Second)
	f.land()
	f.wantState(c.ID, Open)
	if len(f.gh.posted) != 0 || f.mech.HandedOff(c.PR) {
		t.Fatalf("posted %v, handed off %v while audit has not reported", f.gh.posted, f.mech.HandedOff(c.PR))
	}

	f.now = created.Add(LiveLimit)
	f.land()
	f.wantState(c.ID, Failed)
	as := f.alerts()
	if len(as) != 1 || !strings.Contains(as[0].body, "audit") || !strings.Contains(as[0].body, "open") {
		t.Fatalf("alerts = %+v; want one that names audit and open", as)
	}
	if len(f.gh.posted) != 0 || len(f.notices()) != 0 {
		t.Fatalf("posted %v, notices %+v", f.gh.posted, f.notices())
	}
}

// C34: once the check the proposed list adds is green, the change lands.
// A change that removes a check still waits for it, because the list in
// force names it.
func TestC34ChangeWaitsForEachCheckInForce(t *testing.T) {
	t.Run("an added check that reports", func(t *testing.T) {
		f := newLandFixture(t, baseConfig)
		c := f.open("merge.required_checks", strp("gates,audit"), "success")
		f.gh.check(c.Head, "audit", "success")
		f.land()
		f.wantState(c.ID, Handed)
	})
	t.Run("a removed check", func(t *testing.T) {
		f := newLandFixture(t, "merge:\n  mechanism: direct\n  required_checks: [gates, lint]\nstages:\n  implementer: {cap: 3}\n")
		f.gh.required = []string{github.GatesCheck, "lint"}
		c := f.open("merge.required_checks", strp("gates"), "success")
		f.land()
		f.wantState(c.ID, Open)
		f.gh.check(c.Head, "lint", "success")
		f.land()
		f.wantState(c.ID, Handed)
	})
}

// C35: a proposed change is retried with the recorded branch, base_sha and
// the same bytes until it opens. A 5xx or a rate limit leaves it proposed
// for the next tick.
func TestC35ProposedChangeIsRetriedUntilItOpens(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	f.fail = &github.APIError{Method: http.MethodPost, Path: "/pulls", Status: http.StatusBadGateway, Message: "bad gateway"}
	c := f.accept("stages.implementer.cap", strp("5"))
	if c.State != Proposed {
		t.Fatalf("change = %+v; want proposed", c)
	}
	// A commit on main after the command read it changes nothing.
	f.other("README")
	_ = f.svc.Land(context.Background(), f.project, f.mech) // the 502 waits for the next tick
	f.wantState(c.ID, Proposed)
	f.fail = &github.RateLimitError{Until: time.Now().Add(time.Minute)}
	if err := f.svc.Land(context.Background(), f.project, f.mech); err == nil {
		t.Fatal("land under a rate limit answered no error; the tick needs it to stop")
	}
	f.wantState(c.ID, Proposed)

	f.fail = nil
	f.land()
	f.wantState(c.ID, Open)
	if len(f.proposed) != 4 {
		t.Fatalf("%d proposals; want 4", len(f.proposed))
	}
	first := f.proposed[0]
	for i, p := range f.proposed[1:] {
		if p.Branch != c.Branch || p.BaseSHA != first.BaseSHA || string(p.Content) != string(first.Content) ||
			p.Message != first.Message || p.Title != first.Title || p.Body != first.Body {
			t.Fatalf("proposal %d = %+v; want the first again: %+v", i+1, p, first)
		}
	}
	if len(f.gh.prs) != 1 || len(f.alerts()) != 0 {
		t.Fatalf("pull requests %v, alerts %+v; want one PR and no alert", f.gh.prs, f.alerts())
	}
}

// C35: a 422 fails a proposed change at once, with GitHub's answer in its
// detail and one alert.
func TestC35RefusedProposalFailsTheChange(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	f.fail = errors.New("github: the server stopped")
	c := f.accept("stages.implementer.cap", strp("5"))
	f.fail = &github.APIError{Method: http.MethodPost, Path: "/pulls", Status: http.StatusUnprocessableEntity,
		Message: "No commits between main and loop/settings/1"}
	f.land()
	r := f.wantState(c.ID, Failed)
	if !strings.Contains(r.detail, "No commits between main and loop/settings/1") {
		t.Fatalf("detail = %q; want GitHub's answer", r.detail)
	}
	if as := f.alerts(); len(as) != 1 || !strings.Contains(as[0].body, "No commits between") {
		t.Fatalf("alerts = %+v; want one that names GitHub's answer", as)
	}
	if len(f.gh.prs) != 0 || len(f.gh.closed) != 0 {
		t.Fatalf("pull requests %v, closed %v", f.gh.prs, f.gh.closed)
	}
}

// C35: a crash after the proposal opened the pull request and before the
// record moved to open gives one pull request, not two.
func TestC35CrashAfterTheProposalOpensOnePullRequest(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	f.crash = true
	c := f.accept("stages.implementer.cap", strp("5"))
	if c.State != Proposed || len(f.gh.prs) != 1 {
		t.Fatalf("change = %+v with %d PRs; want proposed with one", c, len(f.gh.prs))
	}
	f.land()
	f.wantState(c.ID, Open)
	if len(f.gh.prs) != 1 {
		t.Fatalf("%d pull requests; want 1", len(f.gh.prs))
	}
}

// setCreated moves the change's creation, as a clock that ran on would.
func (f *landFixture) setCreated(id int64, at time.Time) {
	f.t.Helper()
	if err := f.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE settings_changes SET created_at = ? WHERE id = ?`, at.Unix(), id)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

// A handed change has no limit: the mechanism holds it.
func TestHandedChangeOutlivesTheLimit(t *testing.T) {
	f := newLandFixture(t, baseConfig)
	c := f.open("stages.implementer.cap", strp("5"), "success")
	f.land()
	f.setCreated(c.ID, f.now.Add(-3*LiveLimit))
	f.land()
	f.wantState(c.ID, Handed)
	if len(f.alerts()) != 0 {
		t.Fatalf("alerts = %+v", f.alerts())
	}
}
