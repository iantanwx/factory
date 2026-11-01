package github

import (
	"context"
	"encoding/json"
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
	ScopePulls    Scope = "pulls"    // open a PR, reply on a thread; contents read because GitHub reads both refs
	ScopeStatuses Scope = "statuses" // post loop/eligible
	ScopeActions  Scope = "actions"  // rerun failed CI jobs
	ScopeMerge    Scope = "merge"    // merge a PR, use the merge queue, open the Mergify setup PR
	ScopeChecks   Scope = "checks"   // post the loop/qa check run
)

var scopePermissions = map[Scope]map[string]string{
	ScopeRead: {"contents": "read", "pull_requests": "read", "checks": "read", "statuses": "read",
		"actions": "read", "metadata": "read"},
	ScopePush:     {"contents": "write", "workflows": "write", "metadata": "read"},
	ScopePulls:    {"contents": "read", "pull_requests": "write", "metadata": "read"},
	ScopeStatuses: {"statuses": "write", "metadata": "read"},
	ScopeActions:  {"actions": "write", "metadata": "read"},
	ScopeMerge:    {"contents": "write", "pull_requests": "write", "metadata": "read"},
	ScopeChecks:   {"checks": "write", "metadata": "read"},
}

// Permissions returns a copy of the permission subset of scope.
func Permissions(scope Scope) map[string]string { return maps.Clone(scopePermissions[scope]) }

// tokenMargin keeps a token from being used so close to its expiry that a
// slow request fails with a 401 that reads as a permission fault.
const tokenMargin = 2 * time.Minute

type cachedToken struct {
	token   string
	expires time.Time
	grant   Grant
}

// Grant is what GitHub granted one installation token when it minted it.
type Grant struct {
	Repositories []string
	Permissions  map[string]string
}

func (g Grant) String() string {
	perms := make([]string, 0, len(g.Permissions))
	for _, k := range slices.Sorted(maps.Keys(g.Permissions)) {
		perms = append(perms, k+":"+g.Permissions[k])
	}
	return "repositories " + strings.Join(g.Repositories, ", ") + " and permissions " + strings.Join(perms, ", ")
}

// AppKey is one loop App that the service may act as.
type AppKey struct {
	// Owner is the GitHub account that owns the App. Empty when host
	// config does not say: the service asks GitHub.
	Owner string
	ID    int64
	// Key reads the App's PEM key from the encrypted store.
	Key func(ctx context.Context) ([]byte, error)
}

// OneApp is the Apps of a host with a single App whose owner GitHub names.
func OneApp(id int64, key func(ctx context.Context) ([]byte, error)) func() ([]AppKey, error) {
	return func() ([]AppKey, error) { return []AppKey{{ID: id, Key: key}}, nil }
}

// appFor returns the App of owner, and keeps each parsed App in memory. It
// reads the App list when it meets a new owner, so an App that
// `loop register` adds reaches a running server.
func (s *Service) appFor(ctx context.Context, owner string) (*App, error) {
	key := strings.ToLower(owner)
	s.mu.Lock()
	app := s.apps[key]
	s.mu.Unlock()
	if app != nil {
		return app, nil
	}
	keys, err := s.appKeys()
	if err != nil {
		return nil, err
	}
	var ownerless []AppKey
	for _, k := range keys {
		switch {
		case k.Owner == "":
			ownerless = append(ownerless, k)
		case strings.EqualFold(k.Owner, owner):
			return s.keep(ctx, key, k)
		}
	}
	for _, k := range ownerless {
		app, err := s.parse(ctx, k)
		if err != nil {
			return nil, err
		}
		var out struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		if err := s.callAs(ctx, app, Call{Method: http.MethodGet, Path: "/app"}, &out); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if login := strings.ToLower(out.Owner.Login); login != "" && s.apps[login] == nil {
			s.apps[login] = app
		}
		s.mu.Unlock()
		if strings.EqualFold(out.Owner.Login, owner) {
			return app, nil
		}
	}
	return nil, fmt.Errorf("%w for %s; run loop register in a clone of one of its repositories", ErrNotConfigured, owner)
}

func (s *Service) appKeys() ([]AppKey, error) {
	if s.cfg.Apps == nil {
		return nil, ErrNotConfigured
	}
	keys, err := s.cfg.Apps()
	if err != nil {
		return nil, fmt.Errorf("github: read the loop Apps: %w", err)
	}
	return keys, nil
}

func (s *Service) keep(ctx context.Context, owner string, k AppKey) (*App, error) {
	app, err := s.parse(ctx, k)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.apps[owner] = app
	s.mu.Unlock()
	return app, nil
}

func (s *Service) parse(ctx context.Context, k AppKey) (*App, error) {
	if k.ID <= 0 || k.Key == nil {
		return nil, ErrNotConfigured
	}
	pem, err := k.Key(ctx)
	if err != nil {
		return nil, fmt.Errorf("github: read the App private key from the store: %w", err)
	}
	app, err := ParseApp(k.ID, pem)
	clear(pem)
	return app, err
}

// isLoopApp reports whether id is one of the loop's Apps.
func (s *Service) isLoopApp(id int64) bool {
	keys, err := s.appKeys()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(keys, func(k AppKey) bool { return k.ID == id })
}

// AppID returns the id of the App that acts on p.
func (s *Service) AppID(ctx context.Context, p config.Project) (int64, error) {
	app, err := s.appFor(ctx, config.RepoOwner(p.Repo))
	if err != nil {
		return 0, err
	}
	return app.ID, nil
}

// appCall sends one call as the App of owner, with a fresh JWT.
func (s *Service) appCall(ctx context.Context, owner string, c Call, out any) (*Response, error) {
	app, err := s.appFor(ctx, owner)
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

func (s *Service) callAs(ctx context.Context, app *App, c Call, out any) error {
	jwt, err := app.JWT(s.cfg.Now())
	if err != nil {
		return err
	}
	c.Token = jwt
	_, err = s.api.Do(ctx, c, out)
	return err
}

// Installation is the App's installation on one repository, as
// GET /repos/{owner}/{repo}/installation reads it.
type Installation struct {
	ID                  int64             `json:"id"`
	RepositorySelection string            `json:"repository_selection"`
	SuspendedAt         Suspension        `json:"suspended_at"`
	Permissions         map[string]string `json:"permissions"`
}

// Suspension is the suspended_at of an installation. GitHub sends null
// for an active installation. Present tells that null from an answer
// that lacks the field, which cannot show the installation is active.
type Suspension struct {
	Present bool
	At      *time.Time
}

// UnmarshalJSON records that the field was present, null or not.
func (s *Suspension) UnmarshalJSON(b []byte) error {
	s.Present = true
	return json.Unmarshal(b, &s.At)
}

// Installation reads the App's installation on the project repository.
func (s *Service) Installation(ctx context.Context, p config.Project) (Installation, error) {
	var in Installation
	_, err := s.appCall(ctx, config.RepoOwner(p.Repo), Call{Method: http.MethodGet, Path: repoPath(p, "/installation")}, &in)
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
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	_, err := s.appCall(ctx, config.RepoOwner(p.Repo), Call{
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
	grant := Grant{Permissions: minted.Permissions}
	for _, r := range minted.Repositories {
		if !strings.EqualFold(r.FullName, p.Repo) {
			return "", fmt.Errorf("github: the token for %s also reaches %s; the loop refuses it", p.Repo, r.FullName)
		}
		grant.Repositories = append(grant.Repositories, r.FullName)
	}
	s.mu.Lock()
	s.tokens[key] = cachedToken{token: minted.Token, expires: minted.ExpiresAt, grant: grant}
	s.mu.Unlock()
	return minted.Token, nil
}

// lastGrant is what GitHub granted the token of scope for p at its last
// mint, or the zero Grant before any mint.
func (s *Service) lastGrant(p config.Project, scope Scope) Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[p.ID+"/"+string(scope)].grant
}

// allowedPermissions is the most the App may hold (KTD15). actions write
// is allowed because `pr rerun` needs it, and checks write because the
// loop posts loop/qa.
var allowedPermissions = map[string]string{
	"contents": "write", "pull_requests": "write", "statuses": "write", "workflows": "write",
	"checks": "write", "actions": "write", "metadata": "read",
}

// PermissionLevel orders a GitHub App permission level: none, read, write,
// admin. An unknown level is none.
func PermissionLevel(l string) int {
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

// CheckInstallation reads the installation and returns a failed checklist
// line when its owner suspended it, or when it holds any permission beyond
// the granted set (KTD15). The installation may select every repository
// of its owner: the loop acts only on the projects the operator registers.
func (s *Service) CheckInstallation(ctx context.Context, p config.Project) ([]CheckLine, error) {
	in, err := s.Installation(ctx, p)
	if err != nil {
		return nil, err
	}
	active := CheckLine{Check: "installation_active", OK: in.SuspendedAt.Present && in.SuspendedAt.At == nil,
		Detail: "the App's installation is active"}
	switch {
	case !in.SuspendedAt.Present:
		active.Detail = "GitHub's installation answer holds no suspended_at, so the loop cannot tell the installation is active"
	case in.SuspendedAt.At != nil:
		active.Detail = "the App's installation was suspended at " + in.SuspendedAt.At.UTC().Format(time.RFC3339) +
			"; unsuspend it in the owner's GitHub settings"
	}
	var extra []string
	for _, k := range slices.Sorted(maps.Keys(in.Permissions)) {
		want, ok := allowedPermissions[k]
		if !ok || PermissionLevel(in.Permissions[k]) > PermissionLevel(want) {
			extra = append(extra, k+":"+in.Permissions[k])
		}
	}
	return []CheckLine{active, {Check: "installation_permissions", OK: len(extra) == 0,
		Detail: "the App holds permissions beyond the loop's set; remove " + strings.Join(extra, ", ")}}, nil
}
