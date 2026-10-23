package main

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/release"
)

// The App from before Apps had owners gets its owner from GitHub, and
// register waits until the App is installed on the repository.
func TestRegisterFindsTheOwnersAppAndWaitsForTheInstall(t *testing.T) {
	api := gt.NewAPI(t)
	var asks atomic.Int32
	api.Route("GET /repos/"+gt.Repo+"/installation", func(w http.ResponseWriter, _ *http.Request) {
		if asks.Add(1) < 3 {
			gt.WriteJSON(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		gt.WriteJSON(w, 200, map[string]any{"id": gt.InstallID})
	})
	api.Route("GET /app", func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"id": gt.AppID, "html_url": "https://github.com/apps/loop-fixture", "owner": map[string]string{"login": gt.Owner}})
	})
	dir := t.TempDir()
	l := release.Layout{Dir: dir}
	cfg := config.DefaultHost(dir)
	cfg.Credentials.GitHubApp = config.GitHubAppCredentials{AppID: gt.AppID, PrivateKeySecret: release.AppKeySecret}
	if err := config.SaveHost(l.HostConfig(), cfg); err != nil {
		t.Fatal(err)
	}
	var said bytes.Buffer
	h := &hostVerbs{githubAPI: api.URL(), stderr: &said, pollEvery: time.Millisecond,
		secret: func(_ context.Context, _ release.Layout, scope, name string) ([]byte, error) {
			if scope != config.HostScope || name != release.AppKeySecret {
				t.Fatalf("read %s/%s", scope, name)
			}
			return gt.KeyPEM(), nil
		}}
	rep := &RegisterReport{Repo: gt.Repo}
	id, err := h.ensureApp(context.Background(), dir, rep)
	if err != nil || id != gt.AppID || rep.App != "installed" {
		t.Fatalf("ensureApp = %d %v %+v", id, err, rep)
	}
	if !strings.Contains(said.String(), "https://github.com/apps/loop-fixture/installations/new") || asks.Load() != 3 {
		t.Fatalf("asked %d times, said %q", asks.Load(), said.String())
	}
	got, err := config.LoadHost(filepath.Join(dir, config.HostConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := got.Credentials.App(gt.Owner); !ok || a.AppID != gt.AppID || a.PrivateKeySecret != release.AppKeySecret || got.Credentials.GitHubApp.AppID != 0 {
		t.Fatalf("host config Apps = %+v", got.Credentials.Apps())
	}
}
