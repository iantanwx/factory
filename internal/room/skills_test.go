package room

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	beadsfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
	"github.com/iantanwx/loop/internal/userspace"
)

const uiConfig = "ui:\n  kind: web\n  paths: [web/**]\n"

// skillWorld is a room service over a real store, fake beads, the machine
// and a fake agent host, whose checkouts are empty git repositories. Its
// pack holds loop-impeccable, marked conditional on the key ui.
type skillWorld struct {
	t  *testing.T
	bx *beadsfake.Client
	s  *Service
}

func newSkillWorld(t *testing.T, cfg string) *skillWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := config.StoreProjectRow(st)(ctx, config.Project{ID: "alpha", Repo: "iantanwx/alpha", Path: dir}); err != nil {
		t.Fatal(err)
	}
	bx := beadsfake.New("tk")
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	m, err := machine.New(machine.Config{Store: st, Beads: func(string) (beadsx.Client, error) { return bx, nil },
		Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	kek := filepath.Join(dir, "keys", "kek")
	if err := secrets.EnsureKeyFile(kek); err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.NewFileKeyProvider(kek)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := config.ParseProjectConfig([]byte(cfg + "stages:\n  implementer: {}\nmerge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	host := fake.New(filepath.Join(dir, "checkouts"))
	s, err := New(Config{Store: st, Machine: m, Agents: host, Secrets: secrets.New(st, keys), Tokens: &rpc.Tokens{Store: st},
		Checkouts:     func(string) sched.CheckoutHost { return gitInit{t: t, Host: host} },
		ProjectConfig: func(context.Context, string) (*config.ProjectSnapshot, error) { return snap, nil },
		BaseHead:      func(context.Context, string, string) (string, error) { return "abc123", nil },
		Pack: brief.Pack{Version: "v1", FS: fstest.MapFS{
			"loop-brainstorm-room/SKILL.md": {Data: []byte("# runbook\n")},
			"loop-impeccable/SKILL.md":      {Data: []byte("# impeccable\n")},
		}, Conditional: map[string]string{"loop-impeccable": "ui"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &skillWorld{t: t, bx: bx, s: s}
}

// gitInit makes each checkout of the fake host an empty git repository.
type gitInit struct {
	t *testing.T
	*fake.Host
}

func (g gitInit) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	co, err := g.Host.CreateCheckout(ctx, spec)
	if err != nil {
		return co, err
	}
	if out, err := gittest.Command("init", "-q", co.Path).CombinedOutput(); err != nil {
		g.t.Fatalf("git init: %v: %s", err, out)
	}
	return co, nil
}

// item makes an item of the bead type in status, with body.
func (w *skillWorld) item(beadType, status, body string) string {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "A " + beadType, Type: beadType, Description: body})
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: status}); err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

// open opens a room of kind on item, and returns its checkout.
func (w *skillWorld) open(kind, item string) string {
	w.t.Helper()
	flags := map[string]string{"kind": kind, "project": "alpha", "reason": "the operator asked", "score": "4"}
	if item != "" {
		flags["item"] = item
	}
	res := w.s.open(context.Background(), &rpc.Call{Principal: rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord-1"},
		Inv: cli.Invocation{Flags: flags}})
	raw, err := json.Marshal(res.Data)
	if err != nil || res.Exit != cli.ExitDone {
		w.t.Fatalf("room open %v = %+v", flags, res)
	}
	var got struct {
		Room Room `json:"room"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Room.State != Live {
		w.t.Fatalf("room open %v = %s, %v; want a live room", flags, raw, err)
	}
	return got.Room.Checkout
}

// holdsImpeccable reports whether the checkout holds loop-impeccable, and
// fails the test unless its brief agrees with its skill root.
func holdsImpeccable(t *testing.T, what, checkout string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(checkout, brief.SkillRoot(agenthost.Claude), "loop-impeccable"))
	dir := err == nil
	listed := slices.Contains(b.Skills, "loop-impeccable")
	named := b.DesignSkill == "loop-impeccable"
	if dir != listed || dir != named || !slices.Contains(b.Skills, "loop-brainstorm-room") {
		t.Fatalf("%s: directory %v, skills %v, design_skill %q; the brief and the checkout disagree", what, dir, b.Skills, b.DesignSkill)
	}
	return dir
}

// filesBody is a ticket body whose "May change" list holds may.
func filesBody(may ...string) string {
	var b strings.Builder
	b.WriteString("## Files\n\nMay change:\n\n")
	for _, p := range may {
		b.WriteString("- `" + p + "`\n")
	}
	b.WriteString("\n## Done means\n\nIt works.\n")
	return b.String()
}

// C11: a brainstorm room of a project with no ui key holds no
// loop-impeccable.
func TestC11ABrainstormRoomOfAProjectWithNoUIKeyHoldsNoLoopImpeccable(t *testing.T) {
	w := newSkillWorld(t, "")
	if holdsImpeccable(t, "a brainstorm room", w.open("brainstorm", "")) {
		t.Fatal("a brainstorm room of a project with no ui key holds loop-impeccable")
	}
}

// C12: in a project that declares ui, an architect room and a review room
// on a document's pull request hold loop-impeccable.
func TestC12EpicRoomsHoldLoopImpeccableWhenUIIsDeclared(t *testing.T) {
	w := newSkillWorld(t, uiConfig)
	if !holdsImpeccable(t, "an architect room", w.open("architect", w.item("epic", machine.Architecture, ""))) {
		t.Fatal("an architect room holds no loop-impeccable")
	}
	if !holdsImpeccable(t, "a review room on a spec", w.open("review", w.item("epic", machine.SpecReview, ""))) {
		t.Fatal("a review room on a document's pull request holds no loop-impeccable")
	}
}

// C13: in a project that declares ui, a brainstorm room with no item holds
// loop-impeccable, and so does its next session, which holds the epic its
// submit made.
func TestC13TheBrainstormRoomHoldsLoopImpeccableBeforeAndAfterItsSubmit(t *testing.T) {
	w := newSkillWorld(t, uiConfig)
	if !holdsImpeccable(t, "a brainstorm room with no item", w.open("brainstorm", "")) {
		t.Fatal("a brainstorm room with no item holds no loop-impeccable")
	}
	epic := w.item("epic", machine.PRDReview, "")
	if !holdsImpeccable(t, "a brainstorm room on its submitted epic", w.open("brainstorm", epic)) {
		t.Fatal("a brainstorm room on its submitted epic holds no loop-impeccable")
	}
}

// C14: a review room on a ticket's pull request holds loop-impeccable when
// the ticket's "May change" list meets ui.paths, and none when it does not.
func TestC14AReviewRoomOnATicketReadsMayChange(t *testing.T) {
	w := newSkillWorld(t, uiConfig)
	if !holdsImpeccable(t, "a review room on web/src/App.tsx", w.open("review", w.item("task", machine.InReview, filesBody("web/src/App.tsx")))) {
		t.Fatal("a review room on a ticket that changes web/src/App.tsx holds no loop-impeccable")
	}
	if holdsImpeccable(t, "a review room on internal/x.go", w.open("review", w.item("task", machine.InReview, filesBody("internal/x.go")))) {
		t.Fatal("a review room on a ticket that changes internal/x.go holds loop-impeccable")
	}
}

// C18: in a project that declares ui, a decision room on a decision item
// holds no loop-impeccable.
func TestC18ADecisionRoomOnADecisionHoldsNoLoopImpeccable(t *testing.T) {
	w := newSkillWorld(t, uiConfig)
	if holdsImpeccable(t, "a decision room", w.open("decision", w.item("decision", machine.NeedsHuman, ""))) {
		t.Fatal("a decision room on a decision item holds loop-impeccable")
	}
}

// C20: in a project whose ui block names the pin acme-design its design
// skill, and whose store holds the pin, an architect room holds acme-design
// in place of loop-impeccable and names it.
func TestC20AnEpicRoomNamesThePinnedDesignSkill(t *testing.T) {
	acme := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: acme-design\n---\n"), Mode: 0o644}}
	sum, err := pack.TreeHash(acme)
	if err != nil {
		t.Fatal(err)
	}
	pin := config.SkillPin{Name: "acme-design", Repo: "acme/skills", Commit: "0123456789abcdef0123456789abcdef01234567", SHA256: sum}
	w := newSkillWorld(t, uiConfig+"  design_skill: acme-design\nskills:\n  - name: acme-design\n    repo: "+pin.Repo+
		"\n    commit: "+pin.Commit+"\n    path: \"\"\n    sha256: "+pin.SHA256+"\n")
	w.s.cfg.SkillStore = userspace.Store{Dir: filepath.Join(t.TempDir(), "userspace-skills")}
	if err := w.s.cfg.SkillStore.Put("alpha", pin, acme); err != nil {
		t.Fatal(err)
	}
	co := w.open("architect", w.item("epic", machine.Architecture, ""))
	if holdsImpeccable(t, "an architect room", co) {
		t.Fatal("an architect room with a design_skill override holds loop-impeccable")
	}
	raw, err := os.ReadFile(filepath.Join(co, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(co, brief.SkillRoot(agenthost.Claude), "acme-design"))
	if err != nil || b.DesignSkill != "acme-design" {
		t.Fatalf("an architect room: acme-design %v, design_skill %q; want acme-design held and named", err, b.DesignSkill)
	}
}
