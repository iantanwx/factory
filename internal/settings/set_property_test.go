package settings

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// fieldValues are the values the property tries for each field, valid and
// not.
var fieldValues = map[string][]string{
	"enabled":           {"true", "false"},
	"model":             {"claude-opus-5-5", "claude-sonnet-5-5", "opus"},
	"harness":           {"claude", "codex", "Bad"},
	"effort":            {"high", "low", "very high"},
	"run_budget":        {"30m", "1h", "-5m"},
	"schedule":          {"*/5 * * * *", "0 * * * *", "every hour"},
	"cap":               {"0", "1", "2", "3", "8"},
	"runs_per_tick":     {"0", "1", "2", "5"},
	"escalated.model":   {"claude-opus-5-5", "gpt-5-codex"},
	"escalated.harness": {"claude", "codex"},
	"escalated.effort":  {"high", "max"},
	"doc_readers":       {"true", "false", "maybe"},
	"mechanism":         {"direct", "mergify", "github-queue", "nope"},
	"required_checks":   {"gates", "gates,audit", "a,a"},
}

// genConfig writes a valid project file with a comment on each line it
// can hold one.
func genConfig(rng *rand.Rand, k config.Known) string {
	var b strings.Builder
	b.WriteString("# the project's config\nmerge:\n  mechanism: direct # how a change lands\n")
	if rng.IntN(2) == 0 {
		b.WriteString("  required_checks: [gates] # the checks\n")
	}
	b.WriteString("gates:\n  - name: test\n    run: go test ./...\n")
	b.WriteString("stages:\n")
	for _, name := range slices.Concat(k.RunStages, k.ProjectJobs, k.AgentJobs) {
		if rng.IntN(3) != 0 {
			continue
		}
		kind, _ := config.KindOf(name, k)
		fmt.Fprintf(&b, "  # the %s\n  %s:", name, name)
		var lines []string
		switch kind {
		case config.KindBrainstorm:
			if rng.IntN(2) == 0 {
				lines = append(lines, fmt.Sprintf("cap: %d", 1+rng.IntN(9)))
			}
		case config.KindProjectJob:
			if rng.IntN(2) == 0 {
				lines = append(lines, "schedule: '*/7 * * * *'")
			}
		default:
			if rng.IntN(4) == 0 {
				lines = append(lines, "enabled: false")
			}
			if rng.IntN(2) == 0 {
				lines = append(lines, "model: claude-sonnet-5-5")
			}
			if kind != config.KindAgentJob && rng.IntN(2) == 0 {
				lines = append(lines, fmt.Sprintf("cap: %d", 1+rng.IntN(9)))
			}
			if kind == config.KindItemStage && rng.IntN(3) == 0 {
				lines = append(lines, fmt.Sprintf("runs_per_tick: %d", 1+rng.IntN(9)))
			}
		}
		if len(lines) == 0 {
			b.WriteString(" {}\n")
			continue
		}
		b.WriteString("\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "    %s # %s of %s\n", l, strings.SplitN(l, ":", 2)[0], name)
		}
	}
	if rng.IntN(2) == 0 {
		fmt.Fprintf(&b, "pools:\n  reviewers:\n    cap: %d # both reviewers\n", 1+rng.IntN(9))
	}
	return b.String()
}

// C23 and C33: for any valid config, and any key and value the command
// accepts for it, the proposed file's settings equal the base file's
// except the named key's. The enabled rows are among them, so no --unset
// and no other change turns a stage on or off outside its own key. The
// names under stages never lose a name, and gain one only for enabled of
// an unlisted stage, a field of a stage that is on by default, or a field
// of brainstorm or a project job. Every comment of the base file is in the
// new one.
func TestC23ProposedFileDiffersInExactlyTheNamedSetting(t *testing.T) {
	rng := rand.New(rand.NewPCG(23, 23))
	hc := config.DefaultHost("/var/lib/loop")
	hc.AgentJobs = map[string]config.AgentJob{"bug-sweep": {Cap: 1, Schedule: "0 * * * *", Runbook: "docs/bug-sweep.md"}}
	k := config.KnownStages(hc)
	keys := rows(hc, nil, nil)
	accepted, unset := 0, 0
	for i := range 1000 {
		raw := genConfig(rng, k)
		base, err := config.ParseProjectConfig([]byte(raw))
		if err != nil {
			t.Fatalf("case %d: the generated config does not parse: %v\n%s", i, err, raw)
		}
		// One change in three unsets a setting that the file holds.
		key := keys[rng.IntN(len(keys))].Key
		var value *string
		if held := slices.DeleteFunc(rows(hc, base, nil), func(r Row) bool { return r.Source != config.SourceProject }); rng.IntN(3) == 0 {
			key = held[rng.IntN(len(held))].Key
		} else {
			field := key[strings.LastIndex(key, ".")+1:]
			if strings.Contains(key, ".escalated.") {
				field = "escalated." + field
			}
			vs := fieldValues[field]
			value = &vs[rng.IntN(len(vs))]
		}
		e, ref := editSetting(hc, []byte(raw), key, value, nil)
		if ref != nil {
			continue
		}
		accepted++
		if value == nil {
			unset++
		}
		next, err := config.ParseProjectConfig(e.raw)
		if err != nil {
			t.Fatalf("case %d: an accepted %s gives a file that does not parse: %v", i, key, err)
		}
		before, after := rows(hc, base, nil), rows(hc, next, nil)
		capOf := map[string]string{}
		for _, r := range after {
			capOf[r.Key] = r.Value
		}
		for j := range before {
			if before[j].Key == key {
				if before[j] == after[j] {
					t.Fatalf("case %d: %s was accepted and moved neither its value nor its source: %+v", i, key, after[j])
				}
				continue
			}
			// Runs per tick that the file does not set is the stage's cap
			// in force (spec Decisions), so it follows a change of the cap.
			if stage, ok := strings.CutSuffix(before[j].Key, ".runs_per_tick"); ok && key == stage+".cap" &&
				before[j].Source == config.SourceDefault && after[j].Source == config.SourceDefault {
				if after[j].Value != capOf[key] {
					t.Fatalf("case %d: set %s gives %+v, want the cap in force %s", i, key, after[j], capOf[key])
				}
				continue
			}
			// Doc readers that the file does not set follow the doc-reviewer's
			// harness fields (spec factorio-4mb8, Decisions), so the reason
			// they are off follows a change of either.
			if before[j].Key == "stages.doc-reviewer.doc_readers" && strings.HasPrefix(key, "stages.doc-reviewer.") && strings.HasSuffix(key, "harness") &&
				before[j].Source == config.SourceDefault && after[j].Source == config.SourceDefault {
				continue
			}
			// A field of an unlisted stage that is on by default lists the
			// stage, so its enabled row keeps its value and takes its source
			// from the file.
			if stage := strings.Join(strings.Split(key, ".")[:2], "."); before[j].Key == stage+".enabled" && value != nil &&
				before[j].Value == "true" && after[j].Value == "true" &&
				before[j].Source == config.SourceDefault && after[j].Source == config.SourceProject {
				continue
			}
			if before[j] != after[j] {
				t.Fatalf("case %d: set %s %v changed %+v to %+v\nbase:\n%s\nnew:\n%s", i, key, value, before[j], after[j], raw, e.raw)
			}
		}
		was, now := slices.Sorted(maps.Keys(base.Stages)), slices.Sorted(maps.Keys(next.Stages))
		for _, n := range was {
			if !slices.Contains(now, n) {
				t.Fatalf("case %d: set %s removed stages.%s", i, key, n)
			}
		}
		if len(now) > len(was) {
			name := strings.Split(key, ".")[1]
			kind, _ := config.KindOf(name, k)
			gains := strings.HasSuffix(key, ".enabled") || base.StageEnabled(name) || kind == config.KindBrainstorm || kind == config.KindProjectJob
			if len(now) != len(was)+1 || !gains {
				t.Fatalf("case %d: set %s added stages %v to %v", i, key, was, now)
			}
		}
		for _, c := range comments(raw) {
			if !strings.Contains(string(e.raw), c) {
				t.Fatalf("case %d: set %s lost the comment %q\n%s", i, key, c, e.raw)
			}
		}
	}
	if accepted < 300 || unset < 100 {
		t.Fatalf("only %d of 1000 changes were accepted, %d of them --unset; the property checks too few", accepted, unset)
	}
}

// comments returns the text of each comment in s.
func comments(s string) []string {
	var cs []string
	for line := range strings.Lines(s) {
		if i := strings.Index(line, "#"); i >= 0 {
			cs = append(cs, strings.TrimSpace(line[i:]))
		}
	}
	return cs
}
