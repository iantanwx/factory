package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/release"
)

// githubAPI is GitHub's REST root, which register calls as an App.
const githubAPI = "https://api.github.com"

// installWait bounds how long register waits for the operator to install
// an App on the repository.
const installWait = 30 * time.Minute

// ensureApp returns the loop App of the repository's owner, and makes sure
// it is installed on the repository. Each owner has its own private App,
// because a private App installs only on its owner's repositories. When
// the owner has none, register creates one in the browser, as install
// does. The running server reads the new App from host config.
func (h *hostVerbs) ensureApp(ctx context.Context, instance string, rep *RegisterReport) (int64, error) {
	l := release.Layout{Dir: instance}
	owner := config.RepoOwner(rep.Repo)
	cfg, err := config.LoadHost(l.HostConfig())
	if err != nil {
		return 0, err
	}
	creds, ok := cfg.Credentials.App(owner)
	if !ok && cfg.Credentials.GitHubApp.AppID != 0 {
		// The App from before Apps had owners: ask GitHub whose it is, and
		// record the owner.
		legacy := cfg.Credentials.GitHubApp
		var me struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		if _, err := h.asApp(ctx, l, legacy, "/app", &me); err != nil {
			return 0, err
		}
		legacy.Owner = me.Owner.Login
		cfg.Credentials.SetApp(legacy)
		if err := config.SaveHost(l.HostConfig(), cfg); err != nil {
			return 0, err
		}
		creds, ok = cfg.Credentials.App(owner)
	}
	if !ok {
		app, err := h.createApp(ctx, owner)
		if err != nil {
			return 0, err
		}
		if err := release.SaveApp(ctx, l, app); err != nil {
			return 0, err
		}
		rep.App = "created"
		creds = config.GitHubAppCredentials{Owner: app.Owner, AppID: app.ID, PrivateKeySecret: release.AppKeySecretFor(app.Owner)}
	}
	if err := h.waitInstalled(ctx, l, creds, rep.Repo); err != nil {
		return 0, err
	}
	if rep.App == "" {
		rep.App = "installed"
	}
	return creds.AppID, nil
}

// createApp runs the App flow for owner: an organisation's settings page,
// or the operator's own for a user account.
func (h *hostVerbs) createApp(ctx context.Context, owner string) (release.CreatedApp, error) {
	var account struct {
		Type string `json:"type"`
	}
	if err := h.ghJSON(ctx, "", &account, "api", "users/"+owner); err != nil {
		return release.CreatedApp{}, err
	}
	flowOwner := ""
	if account.Type == "Organization" {
		flowOwner = owner
	}
	_, _ = fmt.Fprintf(h.out(), "%s has no loop App yet. Create it in the browser, then install it on its repositories.\n", owner)
	app, err := h.appFlow(flowOwner).Create(ctx)
	if err != nil {
		return release.CreatedApp{}, err
	}
	if !strings.EqualFold(app.Owner, owner) {
		return release.CreatedApp{}, fmt.Errorf("the new App belongs to %s, not %s; create it while signed in to GitHub as %s or as an owner of the organisation", app.Owner, owner, owner)
	}
	return app, nil
}

// waitInstalled returns once the App is installed on repo. Until then it
// names the install page and asks again every few seconds.
func (h *hostVerbs) waitInstalled(ctx context.Context, l release.Layout, creds config.GitHubAppCredentials, repo string) error {
	ctx, cancel := context.WithTimeout(ctx, installWait)
	defer cancel()
	told := false
	for {
		status, err := h.asApp(ctx, l, creds, "/repos/"+repo+"/installation", nil)
		if err == nil {
			return nil
		}
		if status != http.StatusNotFound {
			return err
		}
		if !told {
			var app struct {
				HTMLURL string `json:"html_url"`
			}
			if _, err := h.asApp(ctx, l, creds, "/app", &app); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h.out(), "Install the loop App on %s: %s/installations/new\nWaiting for the installation…\n",
				repo, strings.TrimSuffix(app.HTMLURL, "/"))
			told = true
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the loop App of %s is not installed on %s; install it, then run loop register again", config.RepoOwner(repo), repo)
		case <-time.After(h.poll()):
		}
	}
}

// asApp sends one GET as the App, with a JWT signed by its key, and
// returns the HTTP status.
func (h *hostVerbs) asApp(ctx context.Context, l release.Layout, creds config.GitHubAppCredentials, path string, out any) (int, error) {
	key, err := h.secret(ctx, l, config.HostScope, creds.PrivateKeySecret)
	if err != nil {
		return 0, err
	}
	app, err := github.ParseApp(creds.AppID, key)
	clear(key)
	if err != nil {
		return 0, err
	}
	jwt, err := app.JWT(time.Now())
	if err != nil {
		return 0, err
	}
	base := h.githubAPI
	if base == "" {
		base = githubAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("GitHub answered GET %s with HTTP %d", path, resp.StatusCode)
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, errors.New("GitHub's answer to GET " + path + " is not JSON")
	}
	return resp.StatusCode, nil
}
