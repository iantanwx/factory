package release

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/hexid"
)

// AppListen is where the App flow listens for GitHub's redirect. The port
// is fixed so the operator can forward it ahead of time (ssh -L 47123:...).
const AppListen = "127.0.0.1:47123"

// AppPermissions are the permissions the loop App asks for: exactly the
// set the GitHub checks of `loop doctor` allow (KTD15). actions is write
// because `pr rerun` needs it, and checks because the loop posts loop/qa.
var AppPermissions = map[string]string{
	"contents": "write", "pull_requests": "write", "statuses": "write", "workflows": "write",
	"checks": "write", "actions": "write", "metadata": "read",
}

// CreatedApp is what GitHub returns for a new App.
type CreatedApp struct {
	ID   int64
	Slug string
	// Owner is the account that owns the App.
	Owner string
	PEM   []byte
	// InstallURL is where the operator picks the repositories.
	InstallURL string
}

// AppFlow creates the loop GitHub App through GitHub's manifest flow. The
// operator clicks Create on a page that already holds every setting, and
// GitHub hands back the App id and private key. No webhook: the reactor
// polls, because the host has no public endpoint.
type AppFlow struct {
	// Owner is the organisation that owns the App; empty for the
	// operator's personal account. A private App installs only on its
	// owner's repositories, so each owner gets its own App.
	Owner string
	// Name is the App's name. GitHub needs it unique; the page lets the
	// operator change it.
	Name string
	// Homepage is the App's required homepage URL.
	Homepage string
	// Web and API are GitHub's web and API roots.
	Web, API string
	HTTP     *http.Client
	// Listen is where the redirect lands; AppListen when empty, and any
	// free port when that one is taken.
	Listen string
	// Out is where the operator reads the links.
	Out io.Writer
	// Paste reads the address of the page after Create, for a browser
	// that cannot reach Listen. Nil when stdin is not a terminal.
	Paste io.Reader
	// Open tries to show a URL in a browser. Nil opens nothing.
	Open func(url string)
	// Timeout bounds the wait; GitHub's code lives for an hour.
	Timeout time.Duration
}

// Create runs the flow and returns the new App.
func (f *AppFlow) Create(ctx context.Context) (CreatedApp, error) {
	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}
	addr := f.Listen
	if addr == "" {
		addr = AppListen
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return CreatedApp{}, fmt.Errorf("release: listen for the GitHub redirect: %w", err)
		}
	}
	state := hexid.Random(16)
	base := "http://" + ln.Addr().String()
	page, err := f.page(base+"/callback", state)
	if err != nil {
		_ = ln.Close()
		return CreatedApp{}, err
	}

	var once sync.Once
	done := make(chan result, 1)
	finish := func(r result) { once.Do(func() { done <- r }) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		app, err := f.redeem(r.Context(), r.URL.Query(), state)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		finish(result{app: app})
		http.Redirect(w, r, app.InstallURL, http.StatusSeeOther)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()

	dataURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString(page)
	f.say("\nCreate the loop GitHub App: open %s/ and click Create GitHub App.\n", base)
	f.say("If your browser runs on another machine, forward port %s first (ssh -L %s:%s), or paste this into its address bar:\n\n%s\n\n",
		portOf(ln.Addr()), portOf(ln.Addr()), ln.Addr(), dataURL)
	if f.Paste != nil {
		f.say("If the page after Create does not load, paste its address here and press Enter.\n")
		go f.readPaste(ctx, state, finish)
	}
	if f.Open != nil {
		f.Open(base + "/")
	}

	select {
	case r := <-done:
		if r.err != nil {
			return CreatedApp{}, r.err
		}
		f.say("Created %s. Now install it on each project repository: %s\n", r.app.Slug, r.app.InstallURL)
		if f.Open != nil && f.Paste != nil {
			f.Open(r.app.InstallURL)
		}
		return r.app, nil
	case <-ctx.Done():
		return CreatedApp{}, &Error{Code: CodeAppNotCreated, Rule: "app_not_created",
			Hint: "no GitHub App was created before the wait ended; run loop install again"}
	}
}

// say writes one message for the operator. A lost message changes nothing
// the flow does.
func (f *AppFlow) say(format string, args ...any) { _, _ = fmt.Fprintf(f.Out, format, args...) }

type result struct {
	app CreatedApp
	err error
}

func portOf(a net.Addr) string {
	_, port, _ := net.SplitHostPort(a.String())
	return port
}

// readPaste takes the first line that holds the code: the full address of
// the page after Create, or the code alone.
func (f *AppFlow) readPaste(ctx context.Context, state string, finish func(result)) {
	sc := bufio.NewScanner(f.Paste)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		q := url.Values{"code": {line}, "state": {state}}
		if u, err := url.Parse(line); err == nil && u.Query().Get("code") != "" {
			q = u.Query()
		}
		app, err := f.redeem(ctx, q, state)
		if err != nil {
			f.say("%v; paste the address again\n", err)
			continue
		}
		finish(result{app: app})
		return
	}
}

// redeem exchanges the code from GitHub's redirect for the App.
func (f *AppFlow) redeem(ctx context.Context, q url.Values, state string) (CreatedApp, error) {
	if q.Get("state") != state {
		return CreatedApp{}, errors.New("the redirect does not carry this install's state")
	}
	code := q.Get("code")
	if code == "" || strings.ContainsAny(code, "/?#") {
		return CreatedApp{}, errors.New("the redirect carries no code")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(f.API, "/")+"/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return CreatedApp{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := f.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return CreatedApp{}, fmt.Errorf("release: GitHub did not convert the manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CreatedApp{}, err
	}
	if resp.StatusCode != http.StatusCreated {
		return CreatedApp{}, fmt.Errorf("release: GitHub refused the manifest code: HTTP %d", resp.StatusCode)
	}
	var out struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		PEM     string `json:"pem"`
		HTMLURL string `json:"html_url"`
		Owner   struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return CreatedApp{}, fmt.Errorf("release: read GitHub's App: %w", err)
	}
	if out.ID == 0 || out.PEM == "" || out.HTMLURL == "" || out.Owner.Login == "" {
		return CreatedApp{}, errors.New("release: GitHub's App lacks its id, key, address or owner")
	}
	return CreatedApp{ID: out.ID, Slug: out.Slug, Owner: out.Owner.Login, PEM: []byte(out.PEM),
		InstallURL: strings.TrimSuffix(out.HTMLURL, "/") + "/installations/new"}, nil
}

var appPage = template.Must(template.New("app").Parse(`<!doctype html>
<meta charset="utf-8"><title>Create the loop GitHub App</title>
<form method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>Creating the loop GitHub App on GitHub…</p>
<button type="submit">Continue to GitHub</button>
</form>
<script>document.forms[0].submit()</script>
`))

// page is the form that posts the manifest to GitHub.
func (f *AppFlow) page(redirect, state string) ([]byte, error) {
	manifest, err := json.Marshal(map[string]any{
		"name":            f.Name,
		"url":             f.Homepage,
		"hook_attributes": map[string]any{"url": f.Homepage, "active": false},
		"redirect_url":    redirect,
		// A private App installs only on its owner's repositories, so each
		// owner of a project gets its own App.
		"public":              false,
		"default_permissions": AppPermissions,
	})
	if err != nil {
		return nil, err
	}
	action := strings.TrimSuffix(f.Web, "/") + "/settings/apps/new"
	if f.Owner != "" {
		action = strings.TrimSuffix(f.Web, "/") + "/organizations/" + url.PathEscape(f.Owner) + "/settings/apps/new"
	}
	var b strings.Builder
	err = appPage.Execute(&b, map[string]any{"Action": action + "?state=" + url.QueryEscape(state), "Manifest": string(manifest)})
	return []byte(b.String()), err
}
