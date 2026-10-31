package cli

import (
	"context"
	"strings"
	"testing"
)

// withStageRenames puts pairs in the rename table that the CLI reads, for
// the test.
func withStageRenames(t *testing.T, pairs ...[2]string) {
	t.Helper()
	saved := stageRenames
	stageRenames = func() [][2]string { return pairs }
	t.Cleanup(func() { stageRenames = saved })
}

// C6: each verb that takes a stage name or a room kind refuses an old name
// with USAGE and rule stage_renamed, on the command line and on the wire.
// The hint names the new name, and no handler runs.
func TestC6OldStageNameOrRoomKindRefuses(t *testing.T) {
	withStageRenames(t, [2]string{"old-stage", "implementer"}, [2]string{"old-room", "review"})
	cases := []struct {
		path  string
		args  []string
		flags map[string]string
		line  []string
		want  string
	}{
		{"stage pause", []string{"old-stage"}, map[string]string{"reason": "hold"},
			[]string{"stage", "pause", "old-stage", "--reason", "hold"}, "implementer"},
		{"stage resume", []string{"old-stage"}, nil,
			[]string{"stage", "resume", "old-stage"}, "implementer"},
		{"work peek", nil, map[string]string{"stage": "old-stage"},
			[]string{"work", "peek", "--stage", "old-stage"}, "implementer"},
		{"run list", nil, map[string]string{"stage": "old-stage"},
			[]string{"run", "list", "--stage=old-stage"}, "implementer"},
		{"fault list", nil, map[string]string{"stage": "old-stage"},
			[]string{"fault", "list", "--stage", "old-stage"}, "implementer"},
		{"room open", nil, map[string]string{"kind": "old-room"},
			[]string{"room", "open", "--kind", "old-room"}, "review"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			ran := false
			r := withHandler(t, tc.path, func(context.Context, Invocation) Result {
				ran = true
				return Found(map[string]string{})
			})
			env, code := runJSON(t, r, tc.line...)
			hint, _ := env["hint"].(string)
			if code != int(ExitUndecided) || env["code"] != CodeUsage || env["rule"] != "stage_renamed" || !strings.Contains(hint, tc.want) {
				t.Fatalf("loop %s: exit %d, envelope %v", strings.Join(tc.line, " "), code, env)
			}
			if ran {
				t.Fatal("the handler ran for an old name")
			}

			_, res := r.Validate(tc.path, tc.args, tc.flags, "r")
			if res == nil || res.Refusal == nil || res.Refusal.Code != CodeUsage || res.Refusal.Rule != "stage_renamed" ||
				!strings.Contains(res.Refusal.Hint, tc.want) {
				t.Fatalf("the wire call of %s with an old name = %+v", tc.path, res)
			}
		})
	}
}

// C6: a name that the table does not hold passes the stage check.
func TestC6NewStageNamePasses(t *testing.T) {
	withStageRenames(t, [2]string{"old-stage", "implementer"})
	ran := false
	r := withHandler(t, "stage resume", func(context.Context, Invocation) Result {
		ran = true
		return Found(map[string]string{})
	})
	if env, code := runJSON(t, r, "stage", "resume", "implementer"); code != 0 || !ran {
		t.Fatalf("stage resume implementer: exit %d, envelope %v", code, env)
	}
}
