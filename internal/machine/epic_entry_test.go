package machine

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// C1: an epic enters by its blockers. Each create lands on its entry
// state, or on backlog when the caller sent blockers, and a blocked
// create records where unblock sends the epic.
func TestEpicEntersByItsBlockers(t *testing.T) {
	for _, tc := range []struct {
		verb, blocked, status, entry string
	}{
		{"create_prd", "", "prd_needed", ""},
		{"create_prd", "true", "backlog", "prd_needed"},
		{"create_architecture", "", "architecture", ""},
		{"create_architecture", "true", "backlog", "architecture"},
	} {
		t.Run(tc.verb+"/blocked="+tc.blocked, func(t *testing.T) {
			w := newWorld(t)
			params := map[string]string{}
			if tc.blocked != "" {
				params["blocked"] = tc.blocked
			}
			it := w.show(w.create(KindEpic, tc.verb, params))
			if it.Status != tc.status {
				t.Fatalf("status = %s, want %s", it.Status, tc.status)
			}
			if got := it.Fact("epic_entry"); got != tc.entry {
				t.Fatalf("epic_entry = %q, want %q", got, tc.entry)
			}
		})
	}
}

// C2: unblock lands on the recorded entry, through the coordinator's
// `work unblock`. An epic in backlog with no entry is refused, and stays.
func TestUnblockLandsOnTheRecordedEntry(t *testing.T) {
	for _, tc := range []struct{ create, want string }{
		{"create_prd", "prd_needed"},
		{"create_architecture", "architecture"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			w := newWorld(t)
			id := w.create(KindEpic, tc.create, map[string]string{"blocked": "true"})
			mustOK(t, w.call("work unblock", coord(), []string{id}, nil))
			if got := w.show(id).Status; got != tc.want {
				t.Fatalf("unblock landed on %s, want %s", got, tc.want)
			}
		})
	}
	t.Run("no entry", func(t *testing.T) {
		w := newWorld(t)
		id := w.create(KindEpic, "create_architecture", nil)
		w.force(id, "backlog", nil)
		before := w.show(id)
		ref := wantCode(t, w.call("work unblock", coord(), []string{id}, nil), cli.CodeTransitionNotAllowed)
		if ref.CurrentStatus != "backlog" {
			t.Fatalf("current_status = %s", ref.CurrentStatus)
		}
		if after := w.show(id); after.Status != "backlog" || after.Seq != before.Seq {
			t.Fatalf("the refused unblock changed the epic: %+v", after)
		}
	})
}
