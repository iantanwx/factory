package release

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub converts one manifest code into an App.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/good-code/conversions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 99, "slug": "loop-abc", "pem": "PEM", "html_url": "https://github.com/apps/loop-abc"})
	}))
}

// syncBuffer is a Writer the flow and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestAppFlowFollowsTheRedirect(t *testing.T) {
	gh := fakeGitHub(t)
	defer gh.Close()
	opened := make(chan string, 2)
	f := &AppFlow{Owner: "acme", Name: "loop-abc", Homepage: Homepage, Web: "https://github.com", API: gh.URL,
		Listen: "127.0.0.1:0", Out: io.Discard, Open: func(u string) { opened <- u }, Timeout: 10 * time.Second}

	type out struct {
		app CreatedApp
		err error
	}
	done := make(chan out, 1)
	go func() {
		app, err := f.Create(context.Background())
		done <- out{app, err}
	}()
	start := <-opened

	// The start page posts the manifest to the organisation's App form.
	resp, err := http.Get(start)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	action := regexp.MustCompile(`action="([^"]+)"`).FindSubmatch(page)
	value := regexp.MustCompile(`value="([^"]+)"`).FindSubmatch(page)
	if action == nil || value == nil {
		t.Fatalf("the start page holds no form:\n%s", page)
	}
	target, err := url.Parse(html.UnescapeString(string(action[1])))
	if err != nil || target.Host != "github.com" || target.Path != "/organizations/acme/settings/apps/new" {
		t.Fatalf("form action = %s", action[1])
	}
	var manifest struct {
		Redirect    string            `json:"redirect_url"`
		Public      bool              `json:"public"`
		Hook        map[string]any    `json:"hook_attributes"`
		Permissions map[string]string `json:"default_permissions"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(value[1]))), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Public || manifest.Hook["active"] != false || manifest.Permissions["administration"] != "" ||
		manifest.Permissions["contents"] != "write" || len(manifest.Permissions) != len(AppPermissions) {
		t.Fatalf("manifest = %+v", manifest)
	}

	// A redirect with another state is refused; the real one redirects to
	// the installation page.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bad, err := client.Get(manifest.Redirect + "?code=good-code&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("a forged state got %d", bad.StatusCode)
	}
	good, err := client.Get(manifest.Redirect + "?code=good-code&state=" + target.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	_ = good.Body.Close()
	if good.StatusCode != http.StatusSeeOther || good.Header.Get("Location") != "https://github.com/apps/loop-abc/installations/new" {
		t.Fatalf("callback = %d %s", good.StatusCode, good.Header.Get("Location"))
	}
	r := <-done
	if r.err != nil || r.app.ID != 99 || string(r.app.PEM) != "PEM" {
		t.Fatalf("Create = %+v %v", r.app, r.err)
	}
}

func TestAppFlowTakesAPastedAddress(t *testing.T) {
	gh := fakeGitHub(t)
	defer gh.Close()
	pr, pw := io.Pipe()
	var outBuf syncBuffer
	f := &AppFlow{Name: "loop-abc", Homepage: Homepage, Web: "https://github.com", API: gh.URL,
		Listen: "127.0.0.1:0", Out: &outBuf, Paste: pr, Timeout: 10 * time.Second}
	type out struct {
		app CreatedApp
		err error
	}
	done := make(chan out, 1)
	go func() {
		app, err := f.Create(context.Background())
		done <- out{app, err}
	}()
	// The data: link carries the same form, with the state.
	var state string
	for deadline := time.Now().Add(5 * time.Second); state == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := regexp.MustCompile(`state=([0-9a-f]+)`).FindStringSubmatch(decodeDataURL(t, outBuf.String())); m != nil {
			state = m[1]
		}
	}
	if state == "" {
		t.Fatalf("no data: link with a state in:\n%s", outBuf.String())
	}
	_, _ = io.WriteString(pw, "http://127.0.0.1:47123/callback?code=bad&state="+state+"\n")
	_, _ = io.WriteString(pw, "http://127.0.0.1:47123/callback?code=good-code&state="+state+"\n")
	r := <-done
	if r.err != nil || r.app.ID != 99 {
		t.Fatalf("Create = %+v %v\n%s", r.app, r.err, outBuf.String())
	}
	if !strings.Contains(outBuf.String(), "paste the address again") {
		t.Fatalf("a bad paste was not answered:\n%s", outBuf.String())
	}
}

func TestAppFlowGivesUp(t *testing.T) {
	f := &AppFlow{Name: "n", Homepage: Homepage, Web: "https://github.com", API: "http://127.0.0.1:1",
		Listen: "127.0.0.1:0", Out: io.Discard, Timeout: 50 * time.Millisecond}
	_, err := f.Create(context.Background())
	if re, ok := err.(*Error); !ok || re.Code != CodeAppNotCreated {
		t.Fatalf("Create = %v", err)
	}
}

func decodeDataURL(t *testing.T, s string) string {
	t.Helper()
	m := regexp.MustCompile(`data:text/html;base64,(\S+)`).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return ""
	}
	return html.UnescapeString(string(b))
}
