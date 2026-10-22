package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// fakeGH answers gh calls from a table keyed by the joined arguments and
// records each call with its stdin.
type fakeGH struct {
	answers map[string]string
	calls   []string
	stdin   map[string][]byte
}

func (f *fakeGH) run(_ context.Context, _ string, stdin []byte, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if stdin != nil {
		f.stdin[key] = stdin
	}
	out, ok := f.answers[key]
	if !ok {
		return nil, errors.New(key + ": HTTP 404")
	}
	return []byte(out), nil
}

func TestInitSteps(t *testing.T) {
	gh := &fakeGH{stdin: map[string][]byte{}, answers: map[string]string{
		"gh api repos/me/app/commits/main/check-runs":                              `{"check_runs":[{"name":"test"},{"name":"loop/eligible"},{"name":"test"}]}`,
		"gh api repos/me/app/commits/main/status":                                  `{"statuses":[{"context":"lint"}]}`,
		"gh api repos/me/app/rulesets":                                             `[{"id":7,"name":"other"}]`,
		"gh api -X POST repos/me/app/rulesets --input - --silent":                  ``,
		"gh api -X PUT repos/me/app/contents/.loop/config.yaml --input - --silent": ``,
	}}
	h := &hostVerbs{cmd: gh.run}
	ctx := context.Background()
	rep := InitReport{Repo: "me/app", Branch: "main", Path: "/src/app"}

	ci, err := h.ciChecks(ctx, rep)
	if err != nil || !slices.Equal(ci, []string{"lint", "test"}) {
		t.Fatalf("ci = %v %v", ci, err)
	}
	rep.CI = ci

	got, err := h.ensureProjectConfig(ctx, rep)
	if err != nil || got != "created" {
		t.Fatalf("config = %q %v", got, err)
	}
	var put struct{ Branch, Content string }
	if err := json.Unmarshal(gh.stdin["gh api -X PUT repos/me/app/contents/.loop/config.yaml --input - --silent"], &put); err != nil {
		t.Fatal(err)
	}
	body, _ := base64.StdEncoding.DecodeString(put.Content)
	cfg, err := config.ParseProjectConfig(body)
	if err != nil || put.Branch != "main" || cfg.Merge.Mechanism != config.MergeDirect || !cfg.StageEnabled(config.StageImplementer) {
		t.Fatalf("config %+v on %q: %v\n%s", cfg, put.Branch, err, body)
	}

	got, err = h.ensureRuleset(ctx, rep, 42)
	if err != nil || got != "created" {
		t.Fatalf("ruleset = %q %v", got, err)
	}
	raw := string(gh.stdin["gh api -X POST repos/me/app/rulesets --input - --silent"])
	for _, want := range []string{`"context":"lint"`, `"context":"test"`, `"context":"loop/eligible","integration_id":42`,
		`"required_approving_review_count":0`, `"~DEFAULT_BRANCH"`, `"actor_type":"RepositoryRole"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("the ruleset lacks %s: %s", want, raw)
		}
	}

	// A second run finds the config and updates the ruleset.
	gh.answers["gh api repos/me/app/contents/.loop/config.yaml?ref=main --silent"] = ``
	gh.answers["gh api repos/me/app/rulesets"] = `[{"id":9,"name":"loop"}]`
	gh.answers["gh api -X PUT repos/me/app/rulesets/9 --input - --silent"] = ``
	if got, err := h.ensureProjectConfig(ctx, rep); err != nil || got != "present" {
		t.Fatalf("config again = %q %v", got, err)
	}
	if got, err := h.ensureRuleset(ctx, rep, 42); err != nil || got != "updated" {
		t.Fatalf("ruleset again = %q %v", got, err)
	}
}

func TestProjectID(t *testing.T) {
	for repo, want := range map[string]string{"me/My.App": "my-app", "me/2048": "p-2048", "me/tool_x": "tool_x"} {
		if got := projectID(repo); got != want {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
}
