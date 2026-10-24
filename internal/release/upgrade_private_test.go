package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const ghTestToken = "fake-gh-login-for-the-test"

// privateGitHub stands in for github.com, api.github.com and the asset
// host, as they answer for a private repository (measured 2026-10-23
// against slothware-labs/factorio):
//
//   - github.com/<repo>/releases/{latest/download,download/<v>}/<asset>
//     answers 404 with or without a token.
//   - api.github.com answers 404 without a token, and serves the release
//     with one.
//   - an asset read with Accept: application/octet-stream redirects to a
//     signed URL that needs no token.
type privateGitHub struct {
	rw     *releaseWorld
	latest string
	mu     sync.Mutex
	seen   []string // "<host><path> auth=<bool>"
}

func (g *privateGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	auth := r.Header.Get("Authorization")
	authed := auth == "Bearer "+ghTestToken || auth == "token "+ghTestToken
	g.mu.Lock()
	g.seen = append(g.seen, fmt.Sprintf("%s%s auth=%t", r.URL.Host, r.URL.Path, authed))
	g.mu.Unlock()
	switch r.URL.Host {
	case "objects.githubusercontent.com":
		ver, asset, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/signed/"), "/")
		return g.file(r, ver, asset)
	case "api.github.com":
		if !authed {
			return answer(r, http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
		}
		prefix := "/repos/" + ReleaseRepo + "/releases/"
		p := strings.TrimPrefix(r.URL.Path, prefix)
		switch {
		case p == "latest":
			return g.release(r, g.latest), nil
		case strings.HasPrefix(p, "tags/"):
			return g.release(r, strings.TrimPrefix(p, "tags/")), nil
		case strings.HasPrefix(p, "assets/"):
			ver, asset, ok := g.asset(strings.TrimPrefix(p, "assets/"))
			if !ok {
				return answer(r, http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
			}
			if r.Header.Get("Accept") != "application/octet-stream" {
				return answer(r, http.StatusOK, fmt.Sprintf(`{"name":%q}`, asset), nil), nil
			}
			loc := "https://objects.githubusercontent.com/signed/" + ver + "/" + asset
			return answer(r, http.StatusFound, "", http.Header{"Location": {loc}}), nil
		}
	}
	// github.com, and every other path: a private repository is not found.
	return answer(r, http.StatusNotFound, "Not Found", nil), nil
}

// assetIDs numbers each published file, "<version>/<asset>".
func (g *privateGitHub) assetIDs() []string {
	g.rw.mu.Lock()
	defer g.rw.mu.Unlock()
	var ids []string
	for k := range g.rw.files {
		ids = append(ids, strings.TrimPrefix(k, "/"))
	}
	return ids
}

func (g *privateGitHub) asset(id string) (version, asset string, ok bool) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return "", "", false
	}
	for _, k := range g.assetIDs() {
		if assetID(k) == n {
			version, asset, _ = strings.Cut(k, "/")
			return version, asset, true
		}
	}
	return "", "", false
}

// assetID is a stable number for "<version>/<asset>".
func assetID(key string) int {
	h := 0
	for _, c := range key {
		h = (h*31 + int(c)) % 1_000_000
	}
	return h + 1
}

func (g *privateGitHub) release(r *http.Request, version string) *http.Response {
	type asset struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	var assets []asset
	for _, k := range g.assetIDs() {
		v, name, _ := strings.Cut(k, "/")
		if v != version {
			continue
		}
		id := assetID(k)
		assets = append(assets, asset{ID: id, Name: name,
			URL: fmt.Sprintf("https://api.github.com/repos/%s/releases/assets/%d", ReleaseRepo, id)})
	}
	if len(assets) == 0 {
		return answer(r, http.StatusNotFound, `{"message":"Not Found"}`, nil)
	}
	body, _ := json.Marshal(map[string]any{"tag_name": version, "assets": assets})
	return answer(r, http.StatusOK, string(body), nil)
}

func (g *privateGitHub) file(r *http.Request, version, asset string) (*http.Response, error) {
	g.rw.mu.Lock()
	data, ok := g.rw.files["/"+version+"/"+asset]
	g.rw.mu.Unlock()
	if !ok {
		return answer(r, http.StatusNotFound, "Not Found", nil), nil
	}
	return answer(r, http.StatusOK, string(data), nil), nil
}

func answer(r *http.Request, status int, body string, h http.Header) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r, ProtoMajor: 1, ProtoMinor: 1}
}

// fakeGH puts a gh on PATH whose `gh auth token` prints token, or fails
// as gh does with no login when token is empty. GH_TOKEN carries the same
// login, as gh itself honours it.
func fakeGH(t *testing.T, token string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = token ]; then\n"
	if token == "" {
		script += "  echo 'no oauth token found for github.com' >&2\n  exit 1\n"
	} else {
		script += "  echo '" + token + "'\n  exit 0\n"
	}
	script += "fi\necho \"fake gh: unexpected $*\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", token)
	t.Setenv("GITHUB_TOKEN", "")
}

// TestUpgradeReadsAPrivateReleaseRepoThroughTheGhLogin is factorio-5do:
// `loop upgrade` on a private release repository must find and fetch the
// newest release with the operator's gh login, and an unauthenticated 404
// must not read as "no release".
func TestUpgradeReadsAPrivateReleaseRepoThroughTheGhLogin(t *testing.T) {
	setup := func(t *testing.T) (*instanceWorld, *privateGitHub, Source, *Upgrader) {
		w := newInstance(t)
		rw := newReleaseWorld(t)
		rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
		gh := &privateGitHub{rw: rw, latest: "v1.1.0"}
		src := Source{HTTP: &http.Client{Transport: gh}} // DefaultReleaseBase
		u := w.upgrader(rw)
		u.Source = src
		return w, gh, src, u
	}

	t.Run("the latest lookup sends the gh login", func(t *testing.T) {
		fakeGH(t, ghTestToken)
		_, gh, src, _ := setup(t)
		v, err := src.LatestVersion(context.Background())
		if err != nil || v != "v1.1.0" {
			t.Fatalf("LatestVersion = %q, %v; requests:\n%s", v, err, strings.Join(gh.seen, "\n"))
		}
	})

	t.Run("the release download sends the gh login", func(t *testing.T) {
		fakeGH(t, ghTestToken)
		w, gh, _, u := setup(t)
		rep, err := u.Run(context.Background(), "v1.1.0")
		if err != nil || !rep.Changed {
			t.Fatalf("Run = %+v, %v; requests:\n%s", rep, err, strings.Join(gh.seen, "\n"))
		}
		if data, _ := os.ReadFile(filepath.Join(w.dir, "current", BinaryName)); string(data) != "binary v1.1.0" {
			t.Fatalf("current/loop = %q", data)
		}
		for _, s := range gh.seen {
			if strings.HasPrefix(s, "api.github.com") && !strings.HasSuffix(s, "auth=true") {
				t.Errorf("an API read went without the gh login: %s", s)
			}
		}
	})

	t.Run("an unauthenticated 404 is not no release", func(t *testing.T) {
		fakeGH(t, "")
		_, _, src, u := setup(t)
		if _, err := src.LatestVersion(context.Background()); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("LatestVersion with no gh login = %v; want an error that names the login, not a missing release", err)
		}
		_, err := u.Run(context.Background(), "v1.1.0")
		var re *Error
		if err == nil || (errors.As(err, &re) && (re.Code == "NOT_FOUND" || re.Rule == "no_release")) {
			t.Fatalf("Run with no gh login = %v; want a refusal that names the login, not no_release", err)
		}
	})

	t.Run("no other host gets the gh login", func(t *testing.T) {
		fakeGH(t, ghTestToken)
		w := newInstance(t)
		rw := newReleaseWorld(t)
		rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
		var leaked []string
		var mu sync.Mutex
		inner := rw.srv.Config.Handler
		rw.srv.Config.Handler = http.HandlerFunc(func(resp http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				mu.Lock()
				leaked = append(leaked, r.URL.Path)
				mu.Unlock()
			}
			inner.ServeHTTP(resp, r)
		})
		if _, err := w.upgrader(rw).Run(context.Background(), "v1.1.0"); err != nil {
			t.Fatal(err)
		}
		if len(leaked) > 0 {
			t.Fatalf("a LOOP_RELEASE_BASE host got the gh login on %q", leaked)
		}
	})
}
