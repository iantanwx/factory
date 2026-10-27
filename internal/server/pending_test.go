package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// C17 at the verb: pr pending asks every project and answers the rows
// oldest first. --project reads that project's GitHub only. A --project
// that names no registered project refuses before any GitHub read. A
// project whose read fails answers one row with its error, and the other
// project still answers.
func TestPrPendingAnswersEveryProjectOldestFirst(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	// beta lives on a second fake repository with its own call log. Its
	// registration skips the add hooks, which clone a mirror that the git
	// fake does not hold.
	beta := gt.NewAPI(t)
	w.api.Serve("acme/gadgets", beta)
	host, err := config.LoadHost(w.srv.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	host.Projects = append(host.Projects, config.Project{ID: "beta", Repo: "acme/gadgets", Path: t.TempDir()})
	if err := config.SaveHost(w.srv.HostPath, host); err != nil {
		t.Fatal(err)
	}

	owes := func(project string, world *gt.World) string {
		t.Helper()
		id, res := w.srv.Machine.Create(ctx, machine.CreateRequest{Project: project, Kind: machine.KindTicket, Title: "t", Verb: "create",
			Actor: machine.Server("test")})
		if res.Refusal != nil {
			t.Fatalf("create: %+v", *res.Refusal)
		}
		pr := world.Add(&gt.PR{Head: github.BranchFor(id), HeadSHA: strings.Repeat("a", 40)})
		world.Say(pr.Number, gt.Said{Login: "pat", Association: "COLLABORATOR", Body: "Why 20?"})
		return fmt.Sprintf("%s %s #%d %s", project, id, pr.Number, world.Now().Format(time.RFC3339))
	}
	// alpha's fake stores a person's own pull request first, so beta's
	// comment is the older.
	w.api.World.Add(&gt.PR{Head: "pat/idea", HeadSHA: strings.Repeat("a", 40)})
	b := owes("beta", beta.World)
	a := owes("alpha", w.api.World)

	coord := t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "coord-1", Kind: cli.PrincipalCoordinator,
		Checkout: coord}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(coord)
	w.cli = cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return coord, nil }}).Attach(w.cli)
	rows := func(env map[string]any) []string {
		t.Helper()
		list, ok := env["data"].([]any)
		if env["ok"] != true || !ok {
			t.Fatalf("pr pending = %v", env)
		}
		var out []string
		for _, x := range list {
			r := x.(map[string]any)
			if r["error"] != nil {
				out = append(out, fmt.Sprintf("%s error", r["project"]))
				continue
			}
			out = append(out, fmt.Sprintf("%s %s #%v %s", r["project"], r["item"], r["pr"], r["since"]))
		}
		return out
	}

	if got, want := rows(w.run("pr", "pending")), []string{b, a}; !slices.Equal(got, want) {
		t.Fatalf("pr pending = %v; want %v", got, want)
	}

	read := len(beta.Calls())
	if got, want := rows(w.run("pr", "pending", "--project", "alpha")), []string{a}; !slices.Equal(got, want) {
		t.Fatalf("pr pending --project alpha = %v; want %v", got, want)
	}
	if calls := beta.Calls()[read:]; len(calls) != 0 {
		t.Fatalf("pr pending --project alpha read beta's GitHub: %v", calls)
	}

	readA, readB := len(w.api.Calls()), len(beta.Calls())
	env := w.run("pr", "pending", "--project", "nope")
	if env["code"] != cli.CodeNotFound || env["rule"] != "project_not_found" {
		t.Fatalf("pr pending --project nope = %v", env)
	}
	if len(w.api.Calls()) != readA || len(beta.Calls()) != readB {
		t.Fatalf("an unregistered project read GitHub: %v %v", w.api.Calls()[readA:], beta.Calls()[readB:])
	}

	beta.World.FailNext("GET /repos/"+gt.Repo+"/pulls", 404)
	if got, want := rows(w.run("pr", "pending")), []string{"beta error", a}; !slices.Equal(got, want) {
		t.Fatalf("pr pending with beta's read failing = %v; want %v", got, want)
	}
}
