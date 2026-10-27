package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// judgeKeyValue is the stored judge key. No answer may hold it.
const judgeKeyValue = "tsk-judge-0123456789abcdef"

// fixerConfig answers a project config whose fixer stage is on or off.
func fixerConfig(on bool) func(context.Context, string) (*config.ProjectSnapshot, error) {
	return func(context.Context, string) (*config.ProjectSnapshot, error) {
		return &config.ProjectSnapshot{Config: &config.ProjectConfig{
			Stages: map[string]config.StageConfig{config.StageFixer: {Enabled: &on}}}}, nil
	}
}

// C20: for a project whose config enables the fixer, judge_key passes
// when TYPESAFE_API_KEY is stored for the project or globally, and fails
// naming the command that stores it. It never holds the value. A project
// with no fixer gets no line.
func TestC20TheJudgeKeyLine(t *testing.T) {
	w := doctorWorld(t)
	ctx := context.Background()
	key := func() string {
		docs, err := w.gh.Doctor(ctx, w.srv.Projects)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(docs)
		if strings.Contains(string(raw), judgeKeyValue) {
			t.Fatalf("the answer holds the key: %s", raw)
		}
		l, ok := lineOf(t, docs, "judge_key")
		if !ok {
			return "absent"
		}
		if l.OK {
			return "pass"
		}
		if !strings.Contains(l.Detail, "loop secret set TYPESAFE_API_KEY") {
			t.Fatalf("judge_key detail = %q", l.Detail)
		}
		return "fail"
	}
	set := func(scope string) {
		if err := w.srv.Secrets.Set(ctx, scope, "TYPESAFE_API_KEY", strings.NewReader(judgeKeyValue)); err != nil {
			t.Fatal(err)
		}
	}

	w.gh.ProjectConfig = fixerConfig(true)
	if got := key(); got != "fail" {
		t.Fatalf("no stored key: judge_key %s", got)
	}
	set("alpha")
	if got := key(); got != "pass" {
		t.Fatalf("a project key: judge_key %s", got)
	}
	if err := w.srv.Secrets.Delete(ctx, "alpha", "TYPESAFE_API_KEY"); err != nil {
		t.Fatal(err)
	}
	set(config.GlobalScope)
	if got := key(); got != "pass" {
		t.Fatalf("a global key: judge_key %s", got)
	}
	w.gh.ProjectConfig = fixerConfig(false)
	if got := key(); got != "absent" {
		t.Fatalf("no fixer: judge_key %s", got)
	}
}
