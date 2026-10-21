package fault

import (
	"context"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var (
	runP   = rpc.Principal{Kind: cli.PrincipalRun, RunID: "run-1", Project: "alpha", Item: "tk-1", Stage: "implementer"}
	coordP = rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord-1"}
)

// call builds the call the RPC server hands a handler, through the same
// wire check, so the test sends what an honest CLI sends.
func call(t *testing.T, p rpc.Principal, path string, args []string, flags map[string]string) *rpc.Call {
	t.Helper()
	inv, bad := cli.Surface().Validate(path, args, flags, "req")
	if bad != nil {
		t.Fatalf("%s %v %v: %+v", path, args, flags, bad.Refusal)
	}
	return &rpc.Call{Inv: inv, Principal: p}
}

func reportCall(t *testing.T, text string) *rpc.Call {
	return call(t, runP, "fault report", nil, map[string]string{"file": "/tmp/fault.md",
		cli.ExtraFile: base64.StdEncoding.EncodeToString([]byte(text))})
}

func code(res cli.Result) string {
	if res.Refusal == nil {
		return ""
	}
	return res.Refusal.Code
}

func TestIDsRoundTripAndRefuseOtherShapes(t *testing.T) {
	for _, n := range []int64{1, 9, 10, 12345} {
		if got, ok := ParseID(FormatID(n)); !ok || got != n {
			t.Errorf("%d -> %s -> %d %v", n, FormatID(n), got, ok)
		}
	}
	for _, bad := range []string{"", "F-", "F-0", "F-01", "f-1", "1", "F-1x", "F--1", "E-1"} {
		if _, ok := ParseID(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

// A report records the principal, its project, item and stage and the
// release. Ids rise, and no id is given out twice: not after a delete and
// not across a reopen of the database.
func TestReportRecordsTheReporterAndIDsNeverRepeat(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loop.db")
	st := openStore(t, path)
	s := &Service{Store: st, Release: "v1.3.0"}
	changed := 0
	s.Changed = func() { changed++ }
	res := s.report(ctx, reportCall(t, "the first"))
	f, _ := res.Data.(Fault)
	if res.Refusal != nil || f.ID != "F-1" || f.Status != Open || f.Reporter != cli.PrincipalRun || f.RunID != "run-1" ||
		f.Project != "alpha" || f.Item != "tk-1" || f.Stage != "implementer" || f.Release != "v1.3.0" || f.Installed != "v1.3.0" {
		t.Fatalf("report = %+v %+v", f, res.Refusal)
	}
	if changed != 1 {
		t.Fatalf("changed = %d", changed)
	}
	if f, err := s.Report(ctx, coordP, "the second"); err != nil || f.ID != "F-2" || f.Project != "" {
		t.Fatalf("second = %+v %v", f, err)
	}
	if err := st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM faults WHERE n = 2`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	s.Store = openStore(t, path)
	if f, err := s.Report(ctx, runP, "the third"); err != nil || f.ID != "F-3" {
		t.Fatalf("after a delete and a reopen = %+v %v", f, err)
	}
	if f, err := s.Get(ctx, "F-1"); err != nil || f.Text != "the first" {
		t.Fatalf("F-1 after a reopen = %+v %v", f, err)
	}
	if _, err := s.Get(ctx, "F-2"); err != ErrNotFound {
		t.Fatalf("a deleted fault = %v", err)
	}
}

// The verb refuses an oversize, empty or secret-bearing report, and a call
// without file content. The refusal names the secret, never its value.
func TestReportRefusals(t *testing.T) {
	ctx := context.Background()
	red := secrets.NewRedactor()
	const value = "sv-0d9e8f7a6b5c4d3e2f1a"
	red.SetLive("alpha/API_KEY", value)
	s := &Service{Store: openStore(t, filepath.Join(t.TempDir(), "loop.db")), Redactor: red}
	for name, tc := range map[string]struct {
		call *rpc.Call
		code string
	}{
		"oversize": {reportCall(t, strings.Repeat("x", MaxReportBytes+1)), cli.CodeUsage},
		"empty":    {reportCall(t, "\n \t"), cli.CodeUsage},
		"secret":   {reportCall(t, "the key "+value+" leaked"), cli.CodeSecretInOutput},
		"no file":  {call(t, runP, "fault report", nil, map[string]string{"file": "/tmp/fault.md"}), cli.CodeUsage},
	} {
		res := s.report(ctx, tc.call)
		if code(res) != tc.code {
			t.Errorf("%s = %+v", name, res)
		}
		if res.Refusal != nil && strings.Contains(res.Refusal.Hint, value) {
			t.Errorf("%s: the hint holds the value", name)
		}
	}
	if res := s.list(ctx, call(t, coordP, "fault list", nil, nil)); res.Exit != cli.ExitNothing {
		t.Fatalf("a refused report was stored: %+v", res)
	}
	if res := s.report(ctx, reportCall(t, strings.Repeat("x", MaxReportBytes))); res.Refusal != nil {
		t.Fatalf("a report of exactly the cap = %+v", res.Refusal)
	}
}

// Ack moves open to acknowledged once. Resolve takes exactly one
// resolution from open or acknowledged and stores it. A resolved fault
// takes no other move.
func TestAckAndResolveFollowTheStatuses(t *testing.T) {
	ctx := context.Background()
	s := &Service{Store: openStore(t, filepath.Join(t.TempDir(), "loop.db"))}
	for _, text := range []string{"a", "b", "c", "d"} {
		if _, err := s.Report(ctx, runP, text); err != nil {
			t.Fatal(err)
		}
	}
	ack := func(id string) cli.Result { return s.ack(ctx, call(t, coordP, "fault ack", []string{id}, nil)) }
	resolve := func(id string, flags map[string]string) cli.Result {
		return s.resolve(ctx, call(t, coordP, "fault resolve", []string{id}, flags))
	}
	if res := ack("F-1"); res.Data.(Fault).Status != Acknowledged || res.Data.(Fault).AcknowledgedAt == 0 {
		t.Fatalf("ack = %+v", res)
	}
	if res := ack("F-1"); code(res) != "" || res.Data.(Fault).Status != Acknowledged {
		t.Fatalf("a second ack = %+v", res)
	}
	if res := ack("F-9"); code(res) != cli.CodeNotFound {
		t.Fatalf("ack an unknown fault = %+v", res)
	}
	for _, flags := range []map[string]string{
		{},
		{"fixed-in": "v1", "not-a-fault": "true", "reason": "r"},
		{"not-a-fault": "true"},
		{"duplicate-of": "F-2", "reason": "r"},
		{"duplicate-of": "nope"},
		{"duplicate-of": "F-1"},
	} {
		if res := resolve("F-1", flags); code(res) != cli.CodeUsage {
			t.Errorf("resolve %v = %+v", flags, res)
		}
	}
	if res := resolve("F-1", map[string]string{"fixed-in": "v1.4.0"}); code(res) != "" || res.Data.(Fault).Status != Fixed ||
		res.Data.(Fault).FixedIn != "v1.4.0" || res.Data.(Fault).ResolvedBy != "coordinator:coord-1" {
		t.Fatalf("resolve from acknowledged = %+v", res)
	}
	if res := resolve("F-2", map[string]string{"duplicate-of": "F-7"}); code(res) != cli.CodeNotFound {
		t.Fatalf("a duplicate of an unknown fault = %+v", res)
	}
	if res := resolve("F-2", map[string]string{"duplicate-of": "F-1"}); res.Data.(Fault).Status != Duplicate || res.Data.(Fault).DuplicateOf != "F-1" {
		t.Fatalf("resolve from open = %+v", res)
	}
	if res := resolve("F-3", map[string]string{"not-a-fault": "true", "reason": "a project bug"}); res.Data.(Fault).Status != NotAFault ||
		res.Data.(Fault).Reason != "a project bug" {
		t.Fatalf("not a fault = %+v", res)
	}
	for _, id := range []string{"F-1", "F-2", "F-3"} {
		if res := resolve(id, map[string]string{"fixed-in": "v2"}); code(res) != cli.CodeTransitionNotAllowed {
			t.Errorf("resolve resolved %s = %+v", id, res)
		}
		if res := ack(id); code(res) != cli.CodeTransitionNotAllowed {
			t.Errorf("ack resolved %s = %+v", id, res)
		}
	}
	var pending []Fault
	if err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		pending, err = Pending(ctx, tx)
		return err
	}); err != nil || len(pending) != 1 || pending[0].ID != "F-4" {
		t.Fatalf("pending = %+v %v", pending, err)
	}
}

// A brief names the open, acknowledged and recently fixed faults of its
// project and stage, and no other.
func TestForBriefListsOpenAndRecentlyFixedFaultsOfTheProjectAndStage(t *testing.T) {
	ctx := context.Background()
	s := &Service{Store: openStore(t, filepath.Join(t.TempDir(), "loop.db"))}
	reviewer := runP
	reviewer.Stage = "code-reviewer"
	beta := runP
	beta.Project = "beta"
	for _, p := range []rpc.Principal{runP, runP, runP, runP, runP, reviewer, beta, coordP} {
		if _, err := s.Report(ctx, p, "x"); err != nil {
			t.Fatal(err)
		}
	}
	s.ack(ctx, call(t, coordP, "fault ack", []string{"F-2"}, nil))
	s.resolve(ctx, call(t, coordP, "fault resolve", []string{"F-3"}, map[string]string{"fixed-in": "v1.4.0"}))
	s.resolve(ctx, call(t, coordP, "fault resolve", []string{"F-4"}, map[string]string{"fixed-in": "v1.3.0"}))
	s.resolve(ctx, call(t, coordP, "fault resolve", []string{"F-5"}, map[string]string{"duplicate-of": "F-1"}))
	now := time.Now()
	// F-4 was fixed fifteen days ago.
	if err := s.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE faults SET resolved_at = ? WHERE n = 4`, now.Add(-15*24*time.Hour).UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ForBrief(ctx, s.Store, "alpha", "implementer", now)
	if err != nil {
		t.Fatal(err)
	}
	want := []brief.Fault{{ID: "F-1", Status: Open}, {ID: "F-2", Status: Acknowledged}, {ID: "F-3", Status: Fixed, FixedIn: "v1.4.0"}}
	if len(got) != len(want) {
		t.Fatalf("brief faults = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("brief fault %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
