package github

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// Scope names one permission subset. Each installation token is minted
// for the one project repository with the smallest subset its caller
// needs (KTD15).
type Scope string

// Token scopes.
const (
	ScopeRead     Scope = "read"     // every read: PRs, files, checks, statuses, CI logs
	ScopePush     Scope = "push"     // branch push; workflows because an agent's PR may change CI
	ScopePulls    Scope = "pulls"    // open a PR, reply on a thread
	ScopeStatuses Scope = "statuses" // post loop/eligible
	ScopeActions  Scope = "actions"  // rerun failed CI jobs
)

var scopePermissions = map[Scope]map[string]string{
	ScopeRead: {"contents": "read", "pull_requests": "read", "checks": "read", "statuses": "read",
		"actions": "read", "metadata": "read"},
	ScopePush:     {"contents": "write", "workflows": "write", "metadata": "read"},
	ScopePulls:    {"pull_requests": "write", "metadata": "read"},
	ScopeStatuses: {"statuses": "write", "metadata": "read"},
	ScopeActions:  {"actions": "write", "metadata": "read"},
}

// Permissions returns a copy of the permission subset of scope.
func Permissions(scope Scope) map[string]string { return maps.Clone(scopePermissions[scope]) }

// tokenMargin keeps a token from being used so close to its expiry that a
// slow request fails with a 401 that reads as a permission fault.
const tokenMargin = 2 * time.Minute

type cachedToken struct {
	token   string
	expires time.Time
}

// app parses the App key once and keeps it in memory.
func (s *Service) appIdentity(ctx context.Context) (*App, error) {
	s.mu.Lock()
	app := s.app
	s.mu.Unlock()
	if app != nil {
		return app, nil
	}
	if s.cfg.AppID <= 0 || s.cfg.PrivateKey == nil {
		return nil, ErrNotConfigured
	}
	key, err := s.cfg.PrivateKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("github: read the App private key from the store: %w", err)
	}
	app, err = ParseApp(s.cfg.AppID, key)
	clear(key)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.app = app
	s.mu.Unlock()
	return app, nil
}

// appCall sends one call as the App itself, with a fresh JWT.
func (s *Service) appCall(ctx context.Context, c Call, out any) (*Response, error) {
	app, err := s.appIdentity(ctx)
	if err != nil {
		return nil, err
	}
	jwt, err := app.JWT(s.cfg.Now())
	if err != nil {
		return nil, err
	}
	c.Token = jwt
	return s.api.Do(ctx, c, out)
}

// Installation is the App's installation on one repository, as
// GET /repos/{owner}/{repo}/installation reads it.
type Installation struct {
	ID                  int64             `json:"id"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
}

// Installation reads the App's installation on the project repository.
func (s *Service) Installation(ctx context.Context, p config.Project) (Installation, error) {
	var in Installation
	_, err := s.appCall(ctx, Call{Method: http.MethodGet, Path: repoPath(p, "/installation")}, &in)
	if err != nil {
		return Installation{}, err
	}
	if in.ID <= 0 {
		return Installation{}, fmt.Errorf("github: the installation answer for %s names no id", p.Repo)
	}
	s.mu.Lock()
	s.installs[p.ID] = in.ID
	s.mu.Unlock()
	return in, nil
}

// Token returns an installation token for the one project repository with
// the permission subset of scope. It reuses a token until it nears its
// expiry. The token lives in memory only, and the redactor matches it
// while it lives (KTD27). A failed mint caches nothing.
func (s *Service) Token(ctx context.Context, p config.Project, scope Scope) (string, error) {
	perms, ok := scopePermissions[scope]
	if !ok {
		return "", fmt.Errorf("github: unknown token scope %q", scope)
	}
	key := p.ID + "/" + string(scope)
	now := s.cfg.Now()
	s.mu.Lock()
	ct, cached := s.tokens[key]
	id := s.installs[p.ID]
	s.mu.Unlock()
	if cached && ct.expires.Sub(now) > tokenMargin {
		return ct.token, nil
	}
	if id == 0 {
		in, err := s.Installation(ctx, p)
		if err != nil {
			return "", err
		}
		id = in.ID
	}
	_, name, _ := strings.Cut(p.Repo, "/")
	var minted struct {
		Token        string    `json:"token"`
		ExpiresAt    time.Time `json:"expires_at"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	_, err := s.appCall(ctx, Call{
		Method: http.MethodPost, Path: "/app/installations/" + strconv.FormatInt(id, 10) + "/access_tokens",
		Body: map[string]any{"repositories": []string{name}, "permissions": perms}, Repeatable: true,
	}, &minted)
	if err != nil {
		return "", err
	}
	if minted.Token == "" || minted.ExpiresAt.IsZero() {
		return "", fmt.Errorf("github: the token answer for %s held no usable token or expiry", p.Repo)
	}
	// Register the token with the redactor before anything else can see
	// it, and refuse one that reaches another repository.
	s.cfg.Redactor.SetLive("github/installation_token/"+key, minted.Token)
	for _, r := range minted.Repositories {
		if !strings.EqualFold(r.FullName, p.Repo) {
			return "", fmt.Errorf("github: the token for %s also reaches %s; the loop refuses it", p.Repo, r.FullName)
		}
	}
	s.mu.Lock()
	s.tokens[key] = cachedToken{token: minted.Token, expires: minted.ExpiresAt}
	s.mu.Unlock()
	return minted.Token, nil
}

// allowedPermissions is the most the App may hold (KTD15). actions write
// is allowed because `pr rerun` needs it.
var allowedPermissions = map[string]string{
	"contents": "write", "pull_requests": "write", "statuses": "write", "workflows": "write",
	"checks": "read", "actions": "write", "metadata": "read",
}

func level(l string) int {
	switch l {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	}
	return 0
}

// CheckInstallation reads the installation and returns one checklist line
// for each fault: an installation set to all repositories, or any
// permission beyond the granted set (KTD15).
func (s *Service) CheckInstallation(ctx context.Context, p config.Project) ([]CheckLine, error) {
	in, err := s.Installation(ctx, p)
	if err != nil {
		return nil, err
	}
	lines := []CheckLine{{Check: "installation_repositories", OK: in.RepositorySelection == "selected",
		Detail: fmt.Sprintf("the App's installation must select repositories, not %q; select only the project repositories", in.RepositorySelection)}}
	var extra []string
	for _, k := range slices.Sorted(maps.Keys(in.Permissions)) {
		want, ok := allowedPermissions[k]
		if !ok || level(in.Permissions[k]) > level(want) {
			extra = append(extra, k+":"+in.Permissions[k])
		}
	}
	lines = append(lines, CheckLine{Check: "installation_permissions", OK: len(extra) == 0,
		Detail: "the App holds permissions beyond the loop's set; remove " + strings.Join(extra, ", ")})
	return lines, nil
}
