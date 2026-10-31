package settings

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

const storedSecret = "sk-test-0123456789abcdef"

// setFixture is a fixture whose Service records changes in a store and
// proposes each one to a fake GitHub that counts its calls.
type setFixture struct {
	*fixture
	st       *store.Store
	proposed []github.FileChange
	// fail is the error each proposal answers, when not nil.
	fail error
	// mechanism is the error the merge mechanism's Check answers.
	mechanism error
}

func newSetFixture(t *testing.T, cfg string) *setFixture {
	t.Helper()
	f := &setFixture{fixture: newFixture(t)}
	hc := config.DefaultHost("/var/lib/loop")
	hc.AgentJobs = map[string]config.AgentJob{"triage": {Cap: 1, Schedule: "0 * * * *", Runbook: "docs/triage.md"}}
	f.svc.Host = func(context.Context) (*config.HostConfig, error) { return hc, nil }
	f.commit(cfg, "coordinator")
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.st = st
	red := secrets.NewRedactor()
	red.SetStored([]secrets.NamedValue{{Name: "alpha/API_KEY", Value: storedSecret}})
	f.svc.Store, f.svc.Redactor = st, red
	f.svc.Propose = func(_ context.Context, project string, fc github.FileChange) (int, string, error) {
		if project != "alpha" {
			t.Fatalf("proposed for project %s", project)
		}
		f.proposed = append(f.proposed, fc)
		if f.fail != nil {
			return 0, "", f.fail
		}
		return 40 + len(f.proposed), strings.Repeat("c", 40), nil
	}
	f.svc.CheckMechanism = func(_ context.Context, _ string, m config.MergeMechanism) error {
		if m == "" {
			t.Fatal("checked no mechanism")
		}
		return f.mechanism
	}
	return f
}

func strp(s string) *string { return &s }

func (f *setFixture) set(key string, value *string, reason string) (Change, *cli.Refusal) {
	return f.svc.Set(context.Background(), SetRequest{Project: "alpha", Key: key, Value: value, Reason: reason, Actor: "coordinator"})
}

func (f *setFixture) accept(key string, value *string) Change {
	f.t.Helper()
	c, ref := f.set(key, value, "the reason")
	if ref != nil {
		f.t.Fatalf("set %s = %+v", key, *ref)
	}
	return c
}

func (f *setFixture) rows() int {
	f.t.Helper()
	var n int
	if err := f.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM settings_changes`).Scan(&n)
	}); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// refused asserts code, a hint that names want, and that nothing landed:
// no change row and no call to GitHub.
func (f *setFixture) refused(ref *cli.Refusal, code, want string) {
	f.t.Helper()
	if ref == nil || ref.Code != code || !strings.Contains(ref.Hint, want) {
		f.t.Fatalf("refusal = %+v; want %s whose hint names %q", ref, code, want)
	}
	if n := f.rows(); n != 0 || len(f.proposed) != 0 {
		f.t.Fatalf("a refused change left %d rows and %d proposals", n, len(f.proposed))
	}
}

func (f *setFixture) setState(id int64, state State) {
	f.t.Helper()
	if err := f.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE settings_changes SET state = ? WHERE id = ?`, string(state), id)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

const baseConfig = "merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n"

// C20: a reason of only spaces gets USAGE from the server, and lands
// nothing.
func TestC20BlankReasonIsRefused(t *testing.T) {
	f := newSetFixture(t, baseConfig)
	_, ref := f.set("stages.implementer.cap", strp("4"), "  \n ")
	f.refused(ref, cli.CodeUsage, "--reason")
}

// C25: a reason that holds a stored secret value gets SECRET_IN_OUTPUT,
// and lands nothing.
func TestC25ReasonWithAStoredSecretIsRefused(t *testing.T) {
	f := newSetFixture(t, baseConfig)
	_, ref := f.set("stages.implementer.cap", strp("4"), "the key "+storedSecret+" says so")
	f.refused(ref, cli.CodeSecretInOutput, "alpha/API_KEY")
}

// C21: a value that does not validate gets INVALID_SETTING with a hint that
// names the setting, and lands nothing. So does a change that moves
// neither the value nor the source in force.
func TestC21ValueThatDoesNotValidateIsRefused(t *testing.T) {
	for _, c := range []struct {
		name, key string
		value     *string
		mechanism error
	}{
		{"a cap of 0", "stages.implementer.cap", strp("0"), nil},
		{"an unknown key", "stages.implementr.cap", strp("2"), nil},
		{"a key its stage kind cannot hold", "stages.fixer.runs_per_tick", strp("2"), nil},
		{"a malformed schedule", "stages.implementer.schedule", strp("every hour"), nil},
		{"the value in force", "stages.implementer.cap", strp("3"), nil},
		{"enabled false on an unlisted stage", "stages.reproducer.enabled", strp("false"), nil},
		{"a mechanism whose check fails", "merge.mechanism", strp("mergify"), errors.New("no .mergify.yml on main")},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSetFixture(t, baseConfig)
			f.mechanism = c.mechanism
			_, ref := f.set(c.key, c.value, "the reason")
			f.refused(ref, cli.CodeInvalidSetting, c.key)
		})
	}
}

// C32: a field other than enabled on a run stage, the fixer or an agent
// job that the file does not list is refused, and the hint names the
// enabled setting to change first. Enabled on that stage, and a field of
// brainstorm or of a project job that the file does not list, each land one
// change.
func TestC32FieldOnAnUnlistedStageIsRefused(t *testing.T) {
	for _, c := range []struct{ key, enabled string }{
		{"stages.reproducer.cap", "stages.reproducer.enabled"},
		{"stages.fixer.model", "stages.fixer.enabled"},
		{"stages.triage.schedule", "stages.triage.enabled"},
	} {
		t.Run(c.key, func(t *testing.T) {
			f := newSetFixture(t, baseConfig)
			value := "1"
			if !strings.HasSuffix(c.key, ".cap") {
				value = map[string]string{"model": "claude-sonnet-5-5", "schedule": "*/5 * * * *"}[c.key[strings.LastIndex(c.key, ".")+1:]]
			}
			_, ref := f.set(c.key, &value, "the reason")
			f.refused(ref, cli.CodeInvalidSetting, c.enabled)
		})
	}
	for _, c := range []struct{ key, value, stage string }{
		{"stages.reproducer.enabled", "true", "reproducer: {enabled: true}"},
		{"stages.brainstorm.cap", "2", "brainstorm: {cap: 2}"},
		{"stages.merge-handoff.schedule", "*/5 * * * *", "merge-handoff: {schedule: '*/5 * * * *'}"},
	} {
		t.Run(c.key, func(t *testing.T) {
			f := newSetFixture(t, baseConfig)
			f.accept(c.key, strp(c.value))
			if len(f.proposed) != 1 {
				t.Fatalf("%d proposals", len(f.proposed))
			}
			got, err := config.ParseProjectConfig(f.proposed[0].Content)
			if err != nil {
				t.Fatal(err)
			}
			want, err := config.ParseProjectConfig([]byte(baseConfig + "  " + c.stage + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Stages) != 2 || !equalRows(got, want) {
				t.Fatalf("proposed file:\n%s\nwant only %s added", f.proposed[0].Content, c.stage)
			}
		})
	}
}

func equalRows(a, b *config.ProjectConfig) bool {
	hc := config.DefaultHost("/var/lib/loop")
	ra, rb := rows(hc, a, nil), rows(hc, b, nil)
	if len(ra) != len(rb) {
		return false
	}
	for i := range ra {
		if ra[i] != rb[i] {
			return false
		}
	}
	return true
}

// C22: while a change is proposed or open, the command refuses another
// with SETTINGS_PENDING, and the hint names the change's pull request or
// branch. Once that change is merged or failed, the command accepts one.
func TestC22SecondChangeWaitsForTheFirst(t *testing.T) {
	f := newSetFixture(t, baseConfig)
	f.fail = errors.New("github: 502")
	first := f.accept("stages.implementer.cap", strp("4"))
	if _, ref := f.set("stages.implementer.cap", strp("5"), "the reason"); ref == nil || ref.Code != cli.CodeSettingsPending ||
		!strings.Contains(ref.Hint, first.Branch) {
		t.Fatalf("second change while the first is proposed = %+v", ref)
	}
	f.setState(first.ID, Failed)
	f.fail = nil
	second := f.accept("stages.implementer.cap", strp("5"))
	if _, ref := f.set("stages.implementer.cap", strp("6"), "the reason"); ref == nil || ref.Code != cli.CodeSettingsPending ||
		!strings.Contains(ref.Hint, "#42") {
		t.Fatalf("third change while the second is open = %+v", ref)
	}
	f.setState(second.ID, Merged)
	f.accept("stages.implementer.cap", strp("6"))
	if n := f.rows(); n != 3 {
		t.Fatalf("%d change rows, want 3", n)
	}
}

// C24: the change proposes one commit over the head it read, on its own
// branch. The message's subject names the setting and the value, its body
// holds the reason, and its trailers name the setting, the old value with
// its source, and the actor. The pull request body holds the message.
func TestC24OneCommitNamesTheChangeItsActorAndItsReason(t *testing.T) {
	f := newSetFixture(t, "merge:\n  mechanism: direct\nstages:\n  implementer: {}\n")
	head := f.git("rev-parse", "HEAD")
	c, ref := f.set("stages.implementer.cap", strp("3"), "  Two runs at a time\nkeep the quota.  ")
	if ref != nil {
		t.Fatal(*ref)
	}
	if len(f.proposed) != 1 {
		t.Fatalf("%d proposals", len(f.proposed))
	}
	p := f.proposed[0]
	msg := "chore(settings): set stages.implementer.cap to 3\n\nTwo runs at a time\nkeep the quota.\n\n" +
		"Loop-Setting: stages.implementer.cap\nLoop-Old-Value: 8 (host)\nLoop-Changed-By: coordinator\n"
	if p.Message != msg || p.Title != "chore(settings): set stages.implementer.cap to 3" || !strings.Contains(p.Body, msg) {
		t.Fatalf("proposal message %q title %q body %q; want message %q", p.Message, p.Title, p.Body, msg)
	}
	if p.Branch != "loop/settings/"+itoa(c.ID) || p.BaseSHA != head || p.Path != config.ProjectConfigPath {
		t.Fatalf("proposal = branch %s base %s path %s; want loop/settings/%d over %s", p.Branch, p.BaseSHA, p.Path, c.ID, head)
	}
	if c.State != Open || c.PR != 41 || c.Head != strings.Repeat("c", 40) || c.Branch != p.Branch || c.Setting != "stages.implementer.cap" ||
		c.Old != "8" || c.OldSource != config.SourceHost || *c.New != "3" || c.Actor != "coordinator" {
		t.Fatalf("change = %+v", c)
	}

	f2 := newSetFixture(t, baseConfig)
	f2.accept("stages.implementer.cap", nil)
	if m := f2.proposed[0].Message; !strings.HasPrefix(m, "chore(settings): unset stages.implementer.cap\n\n") ||
		!strings.Contains(m, "Loop-Old-Value: 3 (project)\n") {
		t.Fatalf("unset message = %q", m)
	}
}

// C35: when the pull request does not open, the command answers with the
// change in state proposed, and the store holds it so.
func TestC35ProposeFailureAnswersTheChangeProposed(t *testing.T) {
	f := newSetFixture(t, baseConfig)
	f.fail = errors.New("github: 502")
	c, ref := f.set("stages.implementer.cap", strp("4"), "the reason")
	if ref != nil {
		t.Fatalf("set = %+v; want the change, proposed", *ref)
	}
	if c.State != Proposed || c.PR != 0 || c.Head != "" {
		t.Fatalf("change = %+v; want proposed with no pull request", c)
	}
	var state string
	var pr sql.NullInt64
	if err := f.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state, pr FROM settings_changes WHERE id = ?`, c.ID).Scan(&state, &pr)
	}); err != nil {
		t.Fatal(err)
	}
	if state != string(Proposed) || pr.Valid {
		t.Fatalf("stored state %s pr %v", state, pr)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
