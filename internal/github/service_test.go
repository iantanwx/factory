package github

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// The mirror-backed reader is admission's base reader.
var _ admission.BaseReader = (*Service)(nil)

// Every installation token is minted for the one project repository with
// the smallest permission subset of its scope, and the redactor matches
// it while it lives.
func TestTokenIsScopedToTheOneRepository(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok, err := e.svc.Token(ctx, e.project, ScopeStatuses)
	if err != nil {
		t.Fatal(err)
	}
	mint := e.api.Mints()[0]
	if repos, _ := mint["repositories"].([]any); len(repos) != 1 || repos[0] != "widgets" {
		t.Fatalf("mint repositories = %v, want [widgets]", mint["repositories"])
	}
	perms, _ := mint["permissions"].(map[string]any)
	if len(perms) != 2 || perms["statuses"] != "write" || perms["metadata"] != "read" {
		t.Fatalf("mint permissions = %v", perms)
	}
	if hit, _ := e.red.Find("leak " + tok); !hit {
		t.Fatal("the redactor does not match the live token")
	}
	again, err := e.svc.Token(ctx, e.project, ScopeStatuses)
	if err != nil || again != tok || len(e.api.Minted()) != 1 {
		t.Fatalf("the cached token was not reused: %v %d mints", err, len(e.api.Minted()))
	}
}

func TestTokenThatReachesAnotherRepositoryIsRefused(t *testing.T) {
	e := newEnv(t)
	e.api.Route("POST /app/installations/77/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, map[string]any{"token": "ghs_fixturewidetokenvalue", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			"repositories": []any{map[string]string{"full_name": testRepo}, map[string]string{"full_name": "acme/other"}}})
	})
	if _, err := e.svc.Token(context.Background(), e.project, ScopeRead); err == nil {
		t.Fatal("a token that reaches two repositories was accepted")
	}
	if hit, _ := e.red.Find("ghs_fixturewidetokenvalue"); !hit {
		t.Fatal("the refused token is not redacted")
	}
}

func TestDoctorFailsOnAnExtraPermission(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if bad := failed(e.svc.Doctor(ctx, e.project)); len(bad) != 0 {
		t.Fatalf("a good setup fails: %v", bad)
	}
	e.api.SetInstall("all", map[string]string{"contents": "write", "administration": "read", "checks": "write", "metadata": "read"})
	bad := failed(e.svc.Doctor(ctx, e.project))
	if slices.Contains(bad, "installation_repositories") || !slices.Contains(bad, "installation_permissions") {
		t.Fatalf("doctor = %v", bad)
	}
	for _, l := range e.svc.Doctor(ctx, e.project) {
		if l.Check == "installation_permissions" && (!strings.Contains(l.Detail, "administration") || !strings.Contains(l.Detail, "checks:write")) {
			t.Fatalf("the line does not name each extra permission: %s", l.Detail)
		}
	}
}

// Doctor fails an installation that its owner suspended, and one whose
// answer cannot show that it is active (parity 10.2). An installation
// that selects every repository still passes.
func TestDoctorFailsASuspendedInstallation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	line := func() CheckLine {
		for _, l := range e.svc.Doctor(ctx, e.project) {
			if l.Check == "installation_active" {
				return l
			}
		}
		t.Fatal("doctor has no installation_active line")
		return CheckLine{}
	}
	if l := line(); !l.OK {
		t.Fatalf("an active installation fails: %s", l.Detail)
	}
	e.api.SetInstallField("repository_selection", "all")
	if l := line(); !l.OK {
		t.Fatalf("an active installation on all repositories fails: %s", l.Detail)
	}
	e.api.SetInstallField("suspended_at", "2026-10-16T13:00:00Z")
	if l := line(); l.OK || !strings.Contains(l.Detail, "2026-10-16T13:00:00Z") {
		t.Fatalf("a suspended installation: ok=%v detail=%q", l.OK, l.Detail)
	}
	e.api.DropInstallField("suspended_at")
	if l := line(); l.OK || !strings.Contains(l.Detail, "suspended_at") {
		t.Fatalf("an answer with no suspended_at: ok=%v detail=%q", l.OK, l.Detail)
	}
}

func failed(lines []CheckLine) []string {
	var out []string
	for _, l := range lines {
		if !l.OK {
			out = append(out, l.Check)
		}
	}
	return out
}

// project add fails on a default branch that does not require
// loop/eligible, or that allows direct pushes, and names the missing rule.
func TestAddHookChecksTheBranchRules(t *testing.T) {
	cases := map[string]struct {
		rules []any
		want  []string
	}{
		"no eligible": {[]any{
			map[string]any{"type": "pull_request"},
			map[string]any{"type": "required_status_checks", "parameters": map[string]any{
				"required_status_checks": []any{map[string]any{"context": "ci"}}}},
		}, []string{"eligible_required"}},
		"direct pushes": {[]any{
			map[string]any{"type": "required_status_checks", "parameters": map[string]any{
				"required_status_checks": []any{map[string]any{"context": "ci"},
					map[string]any{"context": EligibleContext, "integration_id": testAppID}}}},
		}, []string{"direct_push_blocked"}},
		"eligible from anyone": {[]any{
			map[string]any{"type": "pull_request"},
			map[string]any{"type": "required_status_checks", "parameters": map[string]any{
				"required_status_checks": []any{map[string]any{"context": "ci"}, map[string]any{"context": EligibleContext}}}},
		}, []string{"eligible_source"}},
		// A bare project has no CI yet: loop doctor reports it, and the
		// add goes ahead.
		"nothing": {[]any{}, []string{"eligible_required", "direct_push_blocked"}},
		// A plan with no rulesets: the direct merge checks CI itself.
		"no rulesets on the plan": {nil, nil},
		// An unknown rule type and unknown fields change nothing: the
		// check asserts the rules, not the layout.
		"extra fields": {[]any{
			map[string]any{"type": "pull_request", "ruleset_id": 9, "new_field": true},
			map[string]any{"type": "code_scanning", "parameters": map[string]any{"x": 1}},
			map[string]any{"type": "required_status_checks", "parameters": map[string]any{"strict": true,
				"required_status_checks": []any{map[string]any{"context": "ci", "x": 1},
					map[string]any{"context": EligibleContext, "integration_id": testAppID}}}},
		}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.api.SetRules(c.rules)
			err := e.svc.AddHook(context.Background(), e.project)
			var re *RulesError
			if c.want == nil {
				if err != nil {
					t.Fatalf("add: %v", err)
				}
				return
			}
			if !errors.As(err, &re) || len(re.Lines) != len(c.want) {
				t.Fatalf("add = %v, want %d lines", err, len(c.want))
			}
			for i, w := range c.want {
				if !strings.HasPrefix(re.Lines[i], "- "+w+":") {
					t.Fatalf("line %d = %q, want %s", i, re.Lines[i], w)
				}
			}
		})
	}
}

// After the add hook the mirror holds the base, and admission's reader
// returns the blob id of each declared path.
// Through the App's token, GitHub answers 403 with an upgrade message
// for a private repository on a free plan. The add goes ahead.
func TestAddHookTakesAPlanWithNoRulesets(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		e := newEnv(t)
		e.api.SetRules(nil)
		e.api.SetNoRulesStatus(status)
		if err := e.svc.AddHook(context.Background(), e.project); err != nil {
			t.Fatalf("%d: add = %v", status, err)
		}
		lines, err := e.svc.CheckRules(context.Background(), e.project)
		if err != nil || len(lines) != 1 || !lines[0].OK || lines[0].Check != "branch_rules" {
			t.Fatalf("%d: rules = %+v %v", status, lines, err)
		}
	}
	// Any other 403 still fails.
	e := newEnv(t)
	e.api.SetRules(nil)
	e.api.SetNoRulesStatus(http.StatusUnauthorized)
	if err := e.svc.AddHook(context.Background(), e.project); err == nil {
		t.Fatal("a 401 on the rules passed")
	}
}

func TestMirrorReadsTheBase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.svc.AddHook(ctx, e.project); err != nil {
		t.Fatal(err)
	}
	files, err := e.svc.Files(ctx, testProject, []string{"README"})
	if err != nil || len(files) != 1 || files["README.md"] == "" {
		t.Fatalf("files = %v, %v", files, err)
	}
	data, sha, err := e.svc.ReadAtBase(ctx, testProject, "README.md")
	if err != nil || string(data) != "hello\n" || sha != e.base {
		t.Fatalf("read at base = %q %s %v", data, sha, err)
	}
	if _, _, err := e.svc.ReadAtBase(ctx, testProject, "missing.md"); err == nil {
		t.Fatal("a missing path read")
	}
}

// One rate-limit answer starts one cooldown that every caller shares: the
// next call sends nothing, and the verb answers exit 4 with retry_after.
func TestRateLimitStartsOneSharedCooldown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Token(ctx, e.project, ScopeRead); err != nil {
		t.Fatal(err)
	}
	e.api.Route("GET /repos/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		writeJSON(w, 429, map[string]string{"message": "slow down"})
	})
	_, err := e.svc.FindPR(ctx, e.project, "loop/tk-1")
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("find = %v, want a rate limit", err)
	}
	before := len(e.api.Calls())
	if _, err := e.svc.Checks(ctx, e.project, e.base); !errors.As(err, &rl) {
		t.Fatalf("a second caller was not held: %v", err)
	}
	if _, err := e.svc.Token(ctx, e.project, ScopePulls); !errors.As(err, &rl) {
		t.Fatalf("a token mint was not held: %v", err)
	}
	if after := len(e.api.Calls()); after != before {
		t.Fatalf("calls went out during the cooldown: %v", e.api.Calls()[before:])
	}
	ref := Refusal(err)
	if ref.Code != cli.CodeGitHubUnavailable || ref.RetryAfterSeconds < 100 {
		t.Fatalf("refusal = %+v", ref)
	}
}

// A read retries a transient answer. A write never repeats.
func TestReadsRetryAndWritesDoNot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	n := 0
	e.api.Route("GET /repos/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n < 3 {
			writeJSON(w, 504, map[string]string{"message": "gateway timeout"})
			return
		}
		writeJSON(w, 200, []any{})
	})
	if _, err := e.svc.FindPR(ctx, e.project, "loop/tk-1"); err != nil || n != 3 {
		t.Fatalf("find: %v after %d attempts", err, n)
	}
	posts := 0
	e.api.Route("POST /repos/"+testRepo+"/statuses/"+e.base, func(w http.ResponseWriter, _ *http.Request) {
		posts++
		writeJSON(w, 502, map[string]string{"message": "bad gateway"})
	})
	err := e.svc.PostStatus(ctx, e.project, e.base, EligibleContext, "success", "x")
	if err == nil || posts != 1 {
		t.Fatalf("post: %v after %d attempts; want one failed attempt", err, posts)
	}
	if Refusal(err).Code != cli.CodeGitHubUnavailable {
		t.Fatalf("refusal = %+v", Refusal(err))
	}
}

// Every outbound write that holds a stored secret, in any encoded form,
// is refused before it leaves: a PR body, a reply, a review comment or a
// label.
func TestOutboundTextWithASecretIsRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	forms := map[string]string{
		"plain":   storedSecret,
		"base64":  base64.StdEncoding.EncodeToString([]byte(storedSecret)),
		"url":     url.QueryEscape("x=" + storedSecret + "&"),
		"json":    `"` + storedSecret + `"`,
		"hex":     strings.ToUpper("736b2d746573742d30313233343536373839616263646566"),
		"wrapped": "see\n" + storedSecret + "\n",
	}
	for name, v := range forms {
		bodies := map[string]Call{
			"pr body": {Method: http.MethodPost, Path: repoPath(e.project, "/pulls"),
				Body: map[string]any{"title": "t", "body": v, "head": "loop/tk-1", "base": "main"}},
			"reply":          {Method: http.MethodPost, Path: repoPath(e.project, "/issues/1/comments"), Body: map[string]string{"body": v}},
			"review comment": {Method: http.MethodPost, Path: repoPath(e.project, "/pulls/1/comments"), Body: map[string]any{"body": v, "path": "a"}},
			"label":          {Method: http.MethodPost, Path: repoPath(e.project, "/issues/1/labels"), Body: map[string]any{"labels": []string{v}}},
		}
		for what, c := range bodies {
			before := len(e.api.Calls())
			_, err := e.svc.repoDo(ctx, e.project, ScopePulls, c, nil)
			if Refusal(err).Code != cli.CodeSecretInOutput {
				t.Errorf("%s in %s form: %v", what, name, err)
			}
			for _, call := range e.api.Calls()[before:] {
				if strings.HasPrefix(call, "POST /repos/") {
					t.Errorf("%s in %s form reached GitHub", what, name)
				}
			}
		}
	}
}

// Each owner has its own App. The service mints a project's token with the
// App of the repository's owner, and asks GitHub for the owner of an App
// whose host config names none.
func TestEachOwnerHasItsOwnApp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }
	wrong := func(context.Context) ([]byte, error) { return nil, errors.New("the other owner's key was read") }
	for name, apps := range map[string][]AppKey{
		"named owner":       {{Owner: "someone-else", ID: 9, Key: wrong}, {Owner: "ACME", ID: testAppID, Key: key}},
		"owner from GitHub": {{Owner: "someone-else", ID: 9, Key: wrong}, {ID: testAppID, Key: key}},
	} {
		svc := e.service()
		svc.cfg.Apps = func() ([]AppKey, error) { return apps, nil }
		if _, err := svc.Token(ctx, e.project, ScopeRead); err != nil {
			t.Fatalf("%s: token: %v", name, err)
		}
		if id, err := svc.AppID(ctx, e.project); err != nil || id != testAppID {
			t.Fatalf("%s: app id = %d %v", name, id, err)
		}
	}
	svc := e.service()
	svc.cfg.Apps = func() ([]AppKey, error) { return []AppKey{{Owner: "someone-else", ID: 9, Key: wrong}}, nil }
	if _, err := svc.Token(ctx, e.project, ScopeRead); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no App for the owner: %v", err)
	}
}
