package gates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// lintGate runs the lint stack gate with run as its command. A non-nil
// allowed names lint.json as the gate's baseline, and an allowed that is
// not empty writes it first. It returns the findings and the gate's log.
func lintGate(t *testing.T, run string, allowed Counts) ([]Finding, string) {
	t.Helper()
	root := t.TempDir()
	lint := config.Command{Name: "lint", Run: run}
	if allowed != nil {
		lint.Baseline = "lint.json"
	}
	if len(allowed) > 0 {
		raw, err := json.Marshal(map[string]Counts{"allowed": allowed})
		if err != nil {
			t.Fatal(err)
		}
		write(t, root, map[string]string{"lint.json": string(raw)})
	}
	var log strings.Builder
	r := &Repo{Root: root, Config: &config.ProjectConfig{Gates: []config.Command{lint}}, Out: &log}
	fs, err := stackGate("lint")(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return fs, log.String()
}

// C3: a ratcheted gate holds each key to its baseline in both directions,
// and the command's exit code changes no result.
func TestC3RatchetedGateHoldsEachKeyToItsBaseline(t *testing.T) {
	cases := []struct {
		name, print string
		want        []string
	}{
		{"above", `a.ts::r\na.ts::r\na.ts::r\n`, []string{"a.ts new_finding"}},
		{"below", `a.ts::r\n`, []string{"a.ts stale_baseline"}},
		{"equal", `a.ts::r\na.ts::r\n`, []string{}},
		{"outside", `a.ts::r\nb.ts::q\na.ts::r\n`, []string{"b.ts new_finding"}},
	}
	for _, c := range cases {
		for _, exit := range []string{"0", "1"} {
			t.Run(c.name+"/exit "+exit, func(t *testing.T) {
				fs, _ := lintGate(t, `printf '`+c.print+`loop:end\n'; exit `+exit, Counts{"a.ts::r": 2})
				got := []string{}
				for _, f := range fs {
					got = append(got, f.File+" "+f.Rule)
				}
				if !slices.Equal(got, c.want) {
					t.Fatalf("findings = %q, want %q: %v", got, c.want, fs)
				}
			})
		}
	}
}

// C3: a gate whose baseline file is absent reads it as empty, and its
// finding names the command that adopts the ratchet.
func TestC3AbsentBaselineNamesTheUpdate(t *testing.T) {
	fs, _ := lintGate(t, `printf 'a.ts::r\nloop:end\n'`, Counts{})
	if len(fs) != 1 || fs[0].Rule != "new_finding" || !strings.Contains(fs[0].Message, "loop gates --only lint --update") {
		t.Fatalf("findings = %v, want one new_finding that names loop gates --only lint --update", fs)
	}
}

// C4: a ratcheted command that prints no end line as the last non-blank
// line of its stdout fails, whatever its exit code. Only the end line
// passes on an empty baseline.
func TestC4SilentCommandNeverReadsAsClean(t *testing.T) {
	cases := []struct {
		run  string
		want []string
	}{
		{`true`, []string{"no_end_line"}},
		{`printf 'a::r\n'`, []string{"no_end_line"}},
		{`printf 'loop:end\na::r\n'`, []string{"no_end_line"}},
		{`printf 'a::r\nloop:end\nloop:end\n'`, []string{"no_end_line"}},
		{`printf 'loop:end\n'`, nil},
		{`printf '\nloop:end\n\n'`, nil},
	}
	for _, c := range cases {
		for _, exit := range []string{"0", "3"} {
			run := c.run + "; exit " + exit
			t.Run(run, func(t *testing.T) {
				fs, _ := lintGate(t, run, Counts{})
				if got := rules(fs); !slices.Equal(got, c.want) {
					t.Fatalf("rules = %q, want %q: %v", got, c.want, fs)
				}
			})
		}
	}
}

// C4: the one jq form of the spec's Decisions, run by the gate on the jq
// of PATH, fails on empty input and on a truncated document, and reads
// each document of a stream.
func TestC4TheJqFormNeverReadsSilenceAsClean(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("the jq cases need jq on PATH: %v", err)
	}
	const form = `jq -rn '[inputs] | select(length > 0) | (.[] | .a[]), "loop:end"'`
	cases := []struct {
		input string
		want  []string
	}{
		{``, []string{"no_end_line"}},
		{`{"a":["x"`, []string{"no_end_line"}},
		{`{"a":["x"]} {"a":["y"]}`, nil},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			fs, _ := lintGate(t, `printf '%s' '`+c.input+`' | `+form, Counts{"x": 1, "y": 1})
			if got := rules(fs); !slices.Equal(got, c.want) {
				t.Fatalf("rules = %q, want %q: %v", got, c.want, fs)
			}
		})
	}
}

// A ratcheted gate reads keys from stdout only. Its stderr streams to the
// log, so a progress line never reads as a key.
func TestRatchetedGateStreamsStderr(t *testing.T) {
	fs, log := lintGate(t, `echo progress >&2; printf 'loop:end\n'`, Counts{})
	if len(fs) != 0 || !strings.Contains(log, "progress") {
		t.Fatalf("findings = %v, log = %q; want none, and progress in the log", fs, log)
	}
}

// C5: a gate with no baseline keeps the exit-code rule and streams its
// stdout, where the same command with a baseline is ratcheted.
func TestC5GateWithNoBaselineKeepsTheExitCodeRule(t *testing.T) {
	cases := []struct {
		run                   string
		plain, ratcheted, log []string
	}{
		{`echo a::r`, nil, []string{"no_end_line"}, []string{"a::r"}},
		{`false`, []string{"command_failed"}, []string{"no_end_line"}, nil},
		{`echo loop:end; false`, []string{"command_failed"}, nil, []string{"loop:end"}},
	}
	for _, c := range cases {
		t.Run(c.run, func(t *testing.T) {
			fs, log := lintGate(t, c.run, nil)
			if got := rules(fs); !slices.Equal(got, c.plain) {
				t.Fatalf("with no baseline, rules = %q, want %q", got, c.plain)
			}
			for _, line := range c.log {
				if !strings.Contains(log, line) {
					t.Fatalf("with no baseline, log = %q, want %q in it", log, line)
				}
			}
			if fs, _ := lintGate(t, c.run, Counts{}); !slices.Equal(rules(fs), c.ratcheted) {
				t.Fatalf("with a baseline, rules = %q, want %q", rules(fs), c.ratcheted)
			}
		})
	}
}

// auditGate runs the audit gate with run as its command. A non-empty
// baseline names audit.json, written from accepted, as the gate's baseline.
// lock commits a change to go.sum first. It returns the findings and the
// checkout's root.
func auditGate(t *testing.T, run, baseline, accepted string, lock bool) ([]Finding, string) {
	t.Helper()
	r := testRepo(t, map[string]string{"audit.json": accepted})
	if lock {
		commit(t, r, "build: add a dependency", map[string]string{"go.sum": "x\n"})
	}
	r.Config = &config.ProjectConfig{Gates: []config.Command{{Name: "audit", Run: run, Baseline: baseline}}}
	fs, err := stackGate("audit")(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return fs, r.Root
}

// strs prints each finding as the gate's log prints it.
func strs(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.String())
	}
	return out
}

// C6: the audit gate counts each advisory id once, and holds the set of
// ids to the accepted set in both directions.
func TestC6AuditGateRatchetsEachAdvisoryIDOnce(t *testing.T) {
	const accepted = `{"accepted": {"GHSA-1": {"reason": "no fix upstream", "date": "2026-10-24"}}}`
	cases := []struct {
		name, print string
		want        []string
	}{
		{"one id printed three times", `GHSA-1\nGHSA-1\nGHSA-1\n`, []string{}},
		{"an id outside the set", `GHSA-1\nGHSA-2\nGHSA-2\n`, []string{"audit.json: new_advisory: GHSA-2 is not accepted. Upgrade the package, or accept the advisory in audit.json with a reason and a date"}},
		{"an accepted id no longer printed", ``, []string{"audit.json: stale_advisory: audit.json accepts GHSA-1, and the audit no longer prints it. Delete its entry"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs, _ := auditGate(t, `printf '`+c.print+`loop:end\n'; exit 1`, "audit.json", accepted, true)
			if got := strs(fs); !slices.Equal(got, c.want) {
				t.Fatalf("findings =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// C7: an accepted advisory with a blank reason or a date that is no
// calendar day fails the gate. Each finding names the id, and the gate
// drops no invalid entry.
func TestC7AcceptedAdvisoryNeedsAReasonAndADate(t *testing.T) {
	cases := []struct {
		reason, date string
		want         []string
	}{
		{"no fix upstream", "2026-10-24", []string{}},
		{"", "2026-10-24", []string{"audit.json: no_reason: GHSA-1 is accepted with no reason. Write why the project accepts it"}},
		{" \t", "2026-10-24", []string{"audit.json: no_reason: GHSA-1 is accepted with no reason. Write why the project accepts it"}},
		{"no fix upstream", "2026-13-01", []string{`audit.json: bad_date: GHSA-1 is accepted on "2026-13-01", which is no calendar day in the form YYYY-MM-DD`}},
		{"no fix upstream", "2026-02-29", []string{`audit.json: bad_date: GHSA-1 is accepted on "2026-02-29", which is no calendar day in the form YYYY-MM-DD`}},
		{"no fix upstream", "24 Oct", []string{`audit.json: bad_date: GHSA-1 is accepted on "24 Oct", which is no calendar day in the form YYYY-MM-DD`}},
		{"no fix upstream", "", []string{`audit.json: bad_date: GHSA-1 is accepted on "", which is no calendar day in the form YYYY-MM-DD`}},
	}
	for _, c := range cases {
		t.Run(c.reason+" "+c.date, func(t *testing.T) {
			accepted := fmt.Sprintf(`{"accepted": {"GHSA-1": {"reason": %q, "date": %q}}}`, c.reason, c.date)
			fs, _ := auditGate(t, `printf 'GHSA-1\nloop:end\n'`, "audit.json", accepted, true)
			if got := strs(fs); !slices.Equal(got, c.want) {
				t.Fatalf("findings =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
	t.Run("two invalid entries", func(t *testing.T) {
		const accepted = `{"accepted": {"GHSA-1": {"reason": "", "date": "2026-10-24"}, "GHSA-2": {"reason": "dev only", "date": "24 Oct"}}}`
		fs, _ := auditGate(t, `printf 'GHSA-1\nGHSA-2\nloop:end\n'`, "audit.json", accepted, true)
		want := []string{"no_reason", "bad_date"}
		if got := rules(fs); !slices.Equal(got, want) {
			t.Fatalf("rules = %q, want %q: %v", got, want, fs)
		}
	})
}

// C8: with no changed lock file, the audit gate passes and runs no
// command, with or without a baseline. A changed lock file runs it, and a
// baseline then holds its ids to the accepted set.
func TestC8NoLockFileRunsNoAudit(t *testing.T) {
	const accepted = `{"accepted": {"GHSA-1": {"reason": "no fix upstream", "date": "2026-10-24"}}}`
	for _, baseline := range []string{"", "audit.json"} {
		for _, lock := range []bool{false, true} {
			t.Run(fmt.Sprintf("baseline %q, lock file changed %v", baseline, lock), func(t *testing.T) {
				fs, root := auditGate(t, `touch ran; printf 'GHSA-1\nloop:end\n'`, baseline, accepted, lock)
				if len(fs) != 0 {
					t.Fatalf("findings = %v, want none", fs)
				}
				if _, err := os.Stat(filepath.Join(root, "ran")); (err == nil) != lock {
					t.Fatalf("the command ran: %v, want %v", err == nil, lock)
				}
			})
		}
	}
}

// updateLint runs the lint gate with --update in root, with lint.json as
// its baseline, and returns the findings and the bytes of lint.json after
// the run.
func updateLint(t *testing.T, root, run string) ([]Finding, string) {
	t.Helper()
	lint := config.Command{Name: "lint", Run: run, Baseline: "lint.json"}
	r := &Repo{Root: root, Config: &config.ProjectConfig{Gates: []config.Command{lint}}, Out: io.Discard, Update: true}
	fs, err := stackGate("lint")(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "lint.json"))
	if err != nil {
		t.Fatal(err)
	}
	return fs, string(raw)
}

// C9: --update lowers each count to what the gate found and deletes each
// key it no longer found. It keeps the old count of a raised key and adds
// no found key, and the gate fails on both.
func TestC9UpdateLowersAndNeverRaises(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"lint.json": `{"allowed": {"a::r": 3, "b::r": 1, "c::r": 1}}`})
	fs, got := updateLint(t, root, `printf 'a::r\nc::r\nd::r\nc::r\na::r\nloop:end\n'`)
	want := "{\n  \"allowed\": {\n    \"a::r\": 2,\n    \"c::r\": 1\n  }\n}\n"
	if got != want {
		t.Fatalf("lint.json =\n%s\nwant\n%s", got, want)
	}
	if got, want := strs(fs), []string{
		"c: new_finding: 2 findings of r, and the baseline allows 1. Fix the code, or raise the count in lint.json with a reason",
		"d: new_finding: 1 findings of r, and the baseline allows 0. Fix the code, or raise the count in lint.json with a reason",
	}; !slices.Equal(got, want) {
		t.Fatalf("findings =\n%q\nwant\n%q", got, want)
	}
}

// C10: with no baseline file, --update adopts the found counts with sorted
// keys, and the gate passes. A second --update writes the same bytes. Zero
// keys found writes an empty set.
func TestC10UpdateAdoptsOnceAndRepeats(t *testing.T) {
	cases := []struct{ print, want string }{
		{`b::r\na::r\na::r\n`, "{\n  \"allowed\": {\n    \"a::r\": 2,\n    \"b::r\": 1\n  }\n}\n"},
		{``, "{\n  \"allowed\": {}\n}\n"},
	}
	for _, c := range cases {
		t.Run(c.print, func(t *testing.T) {
			root := t.TempDir()
			run := `printf '` + c.print + `loop:end\n'`
			for i := range 2 {
				fs, got := updateLint(t, root, run)
				if len(fs) != 0 || got != c.want {
					t.Fatalf("run %d: findings = %v, lint.json =\n%s\nwant none, and\n%s", i+1, fs, got, c.want)
				}
			}
		})
	}
}

// updateAudit runs the audit gate with --update on a change to go.sum,
// with audit.json written from accepted when it is not empty. It returns
// the findings, the gate's log and the checkout's root.
func updateAudit(t *testing.T, run, accepted string) ([]Finding, string, string) {
	t.Helper()
	files := map[string]string{}
	if accepted != "" {
		files["audit.json"] = accepted
	}
	r := testRepo(t, files)
	commit(t, r, "build: add a dependency", map[string]string{"go.sum": "x\n"})
	var log strings.Builder
	r.Config = &config.ProjectConfig{Gates: []config.Command{{Name: "audit", Run: run, Baseline: "audit.json"}}}
	r.Out, r.Update = &log, true
	fs, err := stackGate("audit")(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return fs, log.String(), r.Root
}

// C12: --only audit --update deletes each accepted id that the audit no
// longer prints, and keeps every other entry byte for byte. A new id is not
// written, and fails.
func TestC12AuditUpdateOnlyDeletes(t *testing.T) {
	const accepted = `{"accepted": {"GHSA-1": {"reason": "no fix <upstream> & \"x\"", "date": "2026-10-24"}, "GHSA-2": {"reason": "dev only", "date": "2026-10-25"}}}`
	fs, _, root := updateAudit(t, `printf 'GHSA-1\nGHSA-3\nloop:end\n'`, accepted)
	if got, want := strs(fs), []string{"audit.json: new_advisory: GHSA-3 is not accepted. Upgrade the package, or accept the advisory in audit.json with a reason and a date"}; !slices.Equal(got, want) {
		t.Fatalf("findings =\n%q\nwant\n%q", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(root, "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"accepted\": {\n    \"GHSA-1\": {\n      \"reason\": \"no fix <upstream> & \\\"x\\\"\",\n      \"date\": \"2026-10-24\"\n    }\n  }\n}\n"
	if string(raw) != want {
		t.Fatalf("audit.json =\n%s\nwant\n%s", raw, want)
	}

	t.Run("nothing stale", func(t *testing.T) {
		fs, _, root := updateAudit(t, `printf 'GHSA-1\nGHSA-2\nloop:end\n'`, accepted)
		raw, err := os.ReadFile(filepath.Join(root, "audit.json"))
		if err != nil || len(fs) != 0 || string(raw) != accepted {
			t.Fatalf("findings = %v, audit.json = %s, %v; want none, and the file as written", fs, raw, err)
		}
	})
}

// C12: with no audit baseline file, --update writes no file, and says that
// a person writes each accepted id with a reason.
func TestC12AuditUpdateWritesNoFile(t *testing.T) {
	fs, log, root := updateAudit(t, `printf 'GHSA-1\nloop:end\n'`, "")
	if !slices.Equal(rules(fs), []string{"new_advisory"}) {
		t.Fatalf("rules = %q, want new_advisory: %v", rules(fs), fs)
	}
	if _, err := os.Stat(filepath.Join(root, "audit.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit.json: %v, want no file", err)
	}
	if !strings.Contains(log, "audit.json does not exist, and --update writes no accepted advisory") {
		t.Fatalf("log = %q, want the hand-written reason rule", log)
	}
}
