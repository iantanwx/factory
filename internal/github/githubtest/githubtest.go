// Package githubtest holds synthetic GitHub fakes for tests: a REST API
// that mints App installation tokens and answers PR, CI and status calls,
// and a local smart-HTTP git server (git http-backend) that accepts only
// a token the fake minted. Nothing here reaches the network. The fixtures
// name no real repository, token or host path. The acceptance suite uses
// them too.
package githubtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fixture identity.
const (
	Repo      = "acme/widgets"
	Owner     = "acme"
	AppID     = 4242
	InstallID = 77
	// Eligible is the loop's status context.
	Eligible = "loop/eligible"
)

var (
	keyOnce sync.Once
	key     *rsa.PrivateKey
)

// Key is the fixture App key, made once for each test binary.
func Key() *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		key = k
	})
	return key
}

// KeyPEM is Key in PKCS #1 PEM form.
func KeyPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(Key())})
}

// WriteJSON answers v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// VerifyJWT checks an App JWT against Key.
func VerifyJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&Key().PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(raw, &claims)
	return claims["iss"] == fmt.Sprint(AppID)
}

// API is the synthetic REST API.
type API struct {
	srv *httptest.Server

	mu      sync.Mutex
	routes  map[string]http.HandlerFunc
	calls   []string
	tokens  []string
	mints   []map[string]any
	install map[string]any
	rules   []any
	// noRulesStatus is what the rules endpoint answers with nil rules.
	noRulesStatus int
	World         *World
}

// NewAPI starts the API with a good installation and good branch rules,
// and with an empty PR world.
func NewAPI(t testing.TB) *API {
	a := &API{routes: map[string]http.HandlerFunc{},
		install: map[string]any{"id": InstallID, "repository_selection": "selected", "suspended_at": nil,
			"permissions": map[string]string{"contents": "write", "pull_requests": "write", "statuses": "write",
				"workflows": "write", "checks": "read", "actions": "read", "metadata": "read"}},
		rules: GoodRules(), noRulesStatus: 404,
		World: NewWorld()}
	a.srv = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	return a
}

// GoodRules require CI and loop/eligible from the App, and a PR.
func GoodRules() []any {
	return []any{
		map[string]any{"type": "pull_request", "parameters": map[string]any{"required_approving_review_count": 0}},
		map[string]any{"type": "required_status_checks", "parameters": map[string]any{
			"required_status_checks": []any{
				map[string]any{"context": "ci"},
				map[string]any{"context": Eligible, "integration_id": AppID},
			}}},
	}
}

// URL is the API's base URL.
func (a *API) URL() string { return a.srv.URL }

// Route answers "METHOD /path" with h.
func (a *API) Route(key string, h http.HandlerFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes[key] = h
}

// Calls lists every request as "METHOD /path?query".
func (a *API) Calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// Minted lists every token minted, in order.
func (a *API) Minted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.tokens)
}

// Mints lists the body of every mint.
func (a *API) Mints() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.mints)
}

// SetInstall changes the installation answer.
func (a *API) SetInstall(selection string, perms map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.install = map[string]any{"id": InstallID, "repository_selection": selection, "suspended_at": nil, "permissions": perms}
}

// SetInstallField sets one field of the installation answer.
func (a *API) SetInstallField(key string, v any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	in := maps.Clone(a.install)
	in[key] = v
	a.install = in
}

// DropInstallField removes one field from the installation answer.
func (a *API) DropInstallField(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	in := maps.Clone(a.install)
	delete(in, key)
	a.install = in
}

// SetRules changes the rules of the default branch. Nil answers as GitHub
// does for a plan with no rulesets: 404, or the status SetNoRulesStatus set.
func (a *API) SetRules(rules []any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rules = rules
}

// SetNoRulesStatus sets what the rules endpoint answers for nil rules.
func (a *API) SetNoRulesStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noRulesStatus = status
}

// ValidToken reports whether the API minted tok.
func (a *API) ValidToken(tok string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.tokens, tok)
}

func (a *API) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	a.mu.Lock()
	a.calls = append(a.calls, r.Method+" "+r.URL.RequestURI())
	h := a.routes[key]
	a.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case h != nil:
		if !strings.HasPrefix(r.URL.Path, "/app/") && !a.ValidToken(auth) && !VerifyJWT(auth) {
			WriteJSON(w, 401, map[string]string{"message": "Bad credentials"})
			return
		}
		h(w, r)
	case key == "GET /repos/"+Repo+"/installation":
		if !VerifyJWT(auth) {
			WriteJSON(w, 401, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		a.mu.Lock()
		in := a.install
		a.mu.Unlock()
		WriteJSON(w, 200, in)
	case key == "GET /app":
		if !VerifyJWT(auth) {
			WriteJSON(w, 401, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		WriteJSON(w, 200, map[string]any{"id": AppID, "slug": "loop-fixture", "owner": map[string]string{"login": Owner}})
	case key == fmt.Sprintf("POST /app/installations/%d/access_tokens", InstallID):
		if !VerifyJWT(auth) {
			WriteJSON(w, 401, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		tok := fmt.Sprintf("ghs_fixture%04dtokenvalue", len(a.tokens))
		a.tokens = append(a.tokens, tok)
		a.mints = append(a.mints, body)
		a.mu.Unlock()
		WriteJSON(w, 201, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"permissions": body["permissions"], "repositories": []any{map[string]string{"full_name": Repo}}})
	case !a.ValidToken(auth):
		WriteJSON(w, 401, map[string]string{"message": "Bad credentials"})
	case key == "GET /repos/"+Repo:
		WriteJSON(w, 200, map[string]any{"full_name": Repo, "default_branch": "main"})
	case key == "GET /repos/"+Repo+"/rules/branches/main":
		a.mu.Lock()
		rules, status := a.rules, a.noRulesStatus
		a.mu.Unlock()
		if rules == nil {
			// A private repository on a free plan has no rulesets.
			WriteJSON(w, status, map[string]string{"message": "Upgrade to GitHub Pro or make this repository public to enable this feature."})
			return
		}
		WriteJSON(w, 200, rules)
	case a.World.serve(w, r):
	default:
		WriteJSON(w, 404, map[string]string{"message": "Not Found: " + key})
	}
}

// PR is one PR the world holds.
type PR struct {
	Number    int
	Head      string // branch
	HeadSHA   string
	Base      string
	State     string // open or closed
	Merged    bool
	MergeSHA  string
	Title     string
	Body      string
	Fork      bool
	Draft     bool
	UpdatedAt time.Time
	// Labels and MergeableState serve the merge fakes (merge.go).
	Labels         []string
	MergeableState string
	// Reviewers lists each login requested for review, in order.
	Reviewers []string
}

func (p *PR) json() map[string]any {
	repo := Repo
	if p.Fork {
		repo = "mallory/widgets"
	}
	out := map[string]any{
		"number": p.Number, "html_url": fmt.Sprintf("https://example.invalid/pull/%d", p.Number), "state": p.State,
		"title": p.Title, "body": p.Body, "draft": p.Draft, "merge_commit_sha": p.MergeSHA, "updated_at": p.UpdatedAt.UTC().Format(time.RFC3339),
		"head": map[string]any{"ref": p.Head, "sha": p.HeadSHA, "repo": map[string]any{"full_name": repo}},
		"base": map[string]any{"ref": p.Base, "sha": strings.Repeat("b", 40)},
	}
	out["node_id"], out["labels"], out["mergeable_state"] = nodeID(p.Number), labelsJSON(p.Labels), p.MergeableState
	if p.Merged {
		out["merged_at"] = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// World is the PR side of the API: PRs, check runs, statuses, comments,
// changed files and workflow runs.
type World struct {
	mu       sync.Mutex
	prs      []*PR
	runs     map[string][]map[string]any
	statuses []string
	comments map[int][]map[string]any
	reviews  map[int][]map[string]any
	// summaries holds the reviews of each PR: a review's state and the
	// body a person wrote when they submitted it.
	summaries map[int][]map[string]any
	// threads holds the state GraphQL reads for each review thread, by
	// the id of its root comment.
	threads  map[int64]threadState
	files    int
	failNext map[string]int
	clock    time.Time
	merge    mergeWorld
	// wruns and workflows are the Actions side (runs.go).
	wruns     []*Run
	workflows []string
	// HeadOf answers the head sha of a new PR's branch. A fixed sha when
	// nil.
	HeadOf func(branch string) string
}

// NewWorld returns an empty world.
func NewWorld() *World {
	return &World{runs: map[string][]map[string]any{}, failNext: map[string]int{}, comments: map[int][]map[string]any{},
		reviews: map[int][]map[string]any{}, summaries: map[int][]map[string]any{}, threads: map[int64]threadState{},
		clock: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
}

func (w *World) tick() time.Time {
	w.clock = w.clock.Add(time.Second)
	return w.clock
}

// Add stores a PR and returns it.
func (w *World) Add(pr *PR) *PR {
	w.mu.Lock()
	defer w.mu.Unlock()
	pr.Number = len(w.prs) + 1
	if pr.Base == "" {
		pr.Base = "main"
	}
	if pr.State == "" {
		pr.State = "open"
	}
	pr.UpdatedAt = w.tick()
	w.prs = append(w.prs, pr)
	return pr
}

// Update changes a PR and bumps its update time.
func (w *World) Update(pr *PR, fn func(*PR)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(pr)
	pr.UpdatedAt = w.tick()
}

// PR returns the stored PR numbered n, or nil. A test passes it to Update,
// for example to merge a PR the server opened.
func (w *World) PR(n int) *PR {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pr(strconv.Itoa(n))
}

// PRs lists every PR.
func (w *World) PRs() []PR {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PR, 0, len(w.prs))
	for _, p := range w.prs {
		out = append(out, *p)
	}
	return out
}

// Posted lists every status post as "sha context state".
func (w *World) Posted() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.statuses)
}

// Comments lists the conversation comments of PR n.
func (w *World) Comments(n int) []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.comments[n])
}

// threadState is the state GraphQL reads for one review thread.
type threadState struct{ resolved, outdated, hidden bool }

// SetThread sets the state of the review thread whose root comment is
// root. A hidden thread is one GraphQL does not name. A thread nobody
// set is open and current.
func (w *World) SetThread(root int64, resolved, outdated, hidden bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.threads[root] = threadState{resolved: resolved, outdated: outdated, hidden: hidden}
}

// Unsay deletes comment id of PR n, as a person does on GitHub. A reply
// keeps the in_reply_to_id of a root comment that is gone.
func (w *World) Unsay(n int, id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reviews[n] = slices.DeleteFunc(w.reviews[n], func(c map[string]any) bool { return c["id"] == id })
}

// Said is a comment that somebody other than the loop App wrote on a PR.
type Said struct {
	Login string
	// Association is GitHub's author_association, such as OWNER.
	Association string
	Bot         bool
	// Review makes it a review comment, else a conversation comment. A
	// review comment with InReplyTo answers the thread that comment opened.
	Review    bool
	InReplyTo int64
	// Summary makes it a submitted review with this state, such as
	// APPROVED, and Body the review's summary, which can be empty.
	Summary string
	Body    string
}

// Say adds c to PR n, bumps the PR's update time as GitHub does, and
// returns the comment's id.
func (w *World) Say(n int, c Said) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	kind := "User"
	if c.Bot {
		kind = "Bot"
	}
	m := w.comment(n, c.Body, map[string]any{"login": c.Login, "type": kind}, c.Association)
	switch {
	case c.Summary != "":
		m["state"], m["submitted_at"] = c.Summary, m["created_at"]
		delete(m, "created_at")
		w.summaries[n] = append(w.summaries[n], m)
	case !c.Review:
		w.comments[n] = append(w.comments[n], m)
	default:
		if c.InReplyTo != 0 {
			m["in_reply_to_id"] = c.InReplyTo
		}
		w.reviews[n] = append(w.reviews[n], m)
	}
	if p := w.pr(strconv.Itoa(n)); p != nil {
		p.UpdatedAt = w.clock
	}
	return m["id"].(int64)
}

// comment is one comment of PR n, with an id unique on the PR and the
// world's clock as its creation time.
func (w *World) comment(n int, body string, user map[string]any, assoc string) map[string]any {
	return map[string]any{"id": int64(1000 + len(w.comments[n]) + len(w.reviews[n]) + len(w.summaries[n])), "body": body, "user": user,
		"author_association": assoc, "created_at": w.tick().Format(time.RFC3339)}
}

// SetRuns sets the check runs of a sha.
func (w *World) SetRuns(sha string, runs ...map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runs[sha] = runs
}

// SetFiles sets how many changed files every PR has.
func (w *World) SetFiles(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files = n
}

// FailNext answers the next "METHOD /path" with status once.
func (w *World) FailNext(key string, status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failNext[key] = status
}

// CheckRun is a check-run fixture.
func CheckRun(name, sha, status, conclusion string, started time.Time) map[string]any {
	return map[string]any{"id": started.UnixNano(), "name": name, "head_sha": sha, "status": status, "conclusion": conclusion,
		"started_at": started.Format(time.RFC3339)}
}

var (
	rePull     = regexp.MustCompile(`^/repos/` + Repo + `/pulls/(\d+)$`)
	reRequest  = regexp.MustCompile(`^/repos/` + Repo + `/pulls/(\d+)/requested_reviewers$`)
	rePullSub  = regexp.MustCompile(`^/repos/` + Repo + `/(pulls|issues)/(\d+)/(files|comments|reviews)$`)
	reReplies  = regexp.MustCompile(`^/repos/` + Repo + `/pulls/(\d+)/comments/(\d+)/replies$`)
	reChecks   = regexp.MustCompile(`^/repos/` + Repo + `/commits/([0-9a-f]+)/check-runs$`)
	reCombined = regexp.MustCompile(`^/repos/` + Repo + `/commits/([0-9a-f]+)/status$`)
	reStatus   = regexp.MustCompile(`^/repos/` + Repo + `/statuses/([0-9a-f]+)$`)
)

func (w *World) serve(rw http.ResponseWriter, r *http.Request) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	if n := w.failNext[key]; n != 0 {
		delete(w.failNext, key)
		WriteJSON(rw, n, map[string]string{"message": "synthetic failure"})
		return true
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page == 0 {
		page = 1
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/repos/"+Repo+"/pulls":
		list := slices.Clone(w.prs)
		if h := q.Get("head"); h != "" {
			list = slices.DeleteFunc(list, func(p *PR) bool { return Owner+":"+p.Head != h })
		}
		if st := q.Get("state"); st == "open" || st == "closed" {
			list = slices.DeleteFunc(list, func(p *PR) bool { return p.State != st })
		}
		if q.Get("sort") == "updated" {
			sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
		}
		per, _ := strconv.Atoi(q.Get("per_page"))
		if per == 0 {
			per = 30
		}
		out := []any{}
		for i := (page - 1) * per; i < len(list) && i < page*per; i++ {
			// GitHub's list leaves out the mergeable state, which only
			// the read of one PR computes.
			j := list[i].json()
			delete(j, "mergeable_state")
			out = append(out, j)
		}
		WriteJSON(rw, 200, out)
	case r.Method == http.MethodPost && path == "/repos/"+Repo+"/pulls":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, p := range w.prs {
			if p.Head == body["head"] && p.State == "open" {
				WriteJSON(rw, 422, map[string]string{"message": "A pull request already exists for " + Owner + ":" + p.Head})
				return true
			}
		}
		head := strings.Repeat("c", 40)
		if w.HeadOf != nil {
			head = w.HeadOf(body["head"])
		}
		pr := &PR{Number: len(w.prs) + 1, Head: body["head"], Base: body["base"], State: "open", Title: body["title"],
			Body: body["body"], HeadSHA: head, UpdatedAt: w.tick()}
		w.prs = append(w.prs, pr)
		WriteJSON(rw, 201, pr.json())
	case r.Method == http.MethodGet && rePull.MatchString(path):
		n, _ := strconv.Atoi(rePull.FindStringSubmatch(path)[1])
		if n < 1 || n > len(w.prs) {
			return false
		}
		if strings.Contains(r.Header.Get("Accept"), "diff") {
			rw.WriteHeader(200)
			_, _ = io.WriteString(rw, "diff --git a/x b/x\n")
			return true
		}
		WriteJSON(rw, 200, w.prs[n-1].json())
	case r.Method == http.MethodPatch && rePull.MatchString(path):
		n, _ := strconv.Atoi(rePull.FindStringSubmatch(path)[1])
		if n < 1 || n > len(w.prs) {
			return false
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		pr := w.prs[n-1]
		if t, ok := body["title"]; ok {
			pr.Title = t
		}
		if b, ok := body["body"]; ok {
			pr.Body = b
		}
		pr.UpdatedAt = w.tick()
		WriteJSON(rw, 200, pr.json())
	case r.Method == http.MethodGet && rePullSub.MatchString(path):
		m := rePullSub.FindStringSubmatch(path)
		n, _ := strconv.Atoi(m[2])
		switch m[3] {
		case "files":
			out := []any{}
			for i := (page-1)*100 + 1; i <= w.files && i <= page*100; i++ {
				out = append(out, map[string]any{"filename": fmt.Sprintf("f%03d.txt", i), "status": "added", "additions": 1})
			}
			WriteJSON(rw, 200, out)
		default:
			list := w.comments[n]
			switch {
			case m[3] == "reviews":
				list = w.summaries[n]
			case m[1] == "pulls":
				list = w.reviews[n]
			}
			out := []any{}
			for _, c := range list {
				out = append(out, c)
			}
			WriteJSON(rw, 200, out)
		}
	case r.Method == http.MethodPost && reRequest.MatchString(path):
		p := w.pr(reRequest.FindStringSubmatch(path)[1])
		if p == nil {
			return false
		}
		var body struct {
			Reviewers []string `json:"reviewers"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.Reviewers = append(p.Reviewers, body.Reviewers...)
		WriteJSON(rw, 201, p.json())
	case r.Method == http.MethodPost && (rePullSub.MatchString(path) || reReplies.MatchString(path)):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		var n int
		var thread int64
		if m := rePullSub.FindStringSubmatch(path); m != nil {
			n, _ = strconv.Atoi(m[2])
		} else {
			m := reReplies.FindStringSubmatch(path)
			n, _ = strconv.Atoi(m[1])
			thread, _ = strconv.ParseInt(m[2], 10, 64)
		}
		c := w.comment(n, body["body"], map[string]any{"login": "loop[bot]", "type": "Bot"}, "NONE")
		c["performed_via_github_app"] = map[string]any{"id": AppID}
		if thread == 0 {
			w.comments[n] = append(w.comments[n], c)
		} else {
			c["in_reply_to_id"] = thread
			w.reviews[n] = append(w.reviews[n], c)
		}
		WriteJSON(rw, 201, c)
	case r.Method == http.MethodGet && reChecks.MatchString(path):
		runs := w.runs[reChecks.FindStringSubmatch(path)[1]]
		if runs == nil {
			runs = []map[string]any{}
		}
		WriteJSON(rw, 200, map[string]any{"total_count": len(runs), "check_runs": runs})
	case r.Method == http.MethodGet && reCombined.MatchString(path):
		WriteJSON(rw, 200, map[string]any{"statuses": []any{}})
	case r.Method == http.MethodPost && reStatus.MatchString(path):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.statuses = append(w.statuses, reStatus.FindStringSubmatch(path)[1]+" "+body["context"]+" "+body["state"])
		WriteJSON(rw, 201, map[string]any{"state": body["state"]})
	case w.serveRuns(rw, r):
	default:
		return w.serveMerge(rw, r)
	}
	return true
}

// GitServer is a local smart-HTTP git server: git http-backend behind an
// Authorization check that accepts only a token the API minted.
type GitServer struct {
	srv  *httptest.Server
	root string

	mu   sync.Mutex
	auth []string
}

// NewGitServer starts the server and creates the bare remote of Repo with
// a main branch that holds one commit. It skips the test when git or git
// http-backend is missing.
func NewGitServer(t testing.TB, api *API) *GitServer {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skipf("git is not available: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git-http-backend is not available: %v", err)
	}
	g := &GitServer{root: t.TempDir()}
	h := &cgi.Handler{Path: backend, Env: []string{
		"GIT_PROJECT_ROOT=" + g.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=loop",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || !api.ValidToken(pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		g.mu.Lock()
		g.auth = append(g.auth, pass)
		g.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	remote := g.RemoteDir()
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, filepath.Dir(remote), "init", "-q", "--bare", "-b", "main", remote)
	Git(t, remote, "config", "http.receivepack", "true")
	return g
}

// Base is the server's URL with no repository.
func (g *GitServer) Base() string { return g.srv.URL }

// URL is the explicit URL of a repository.
func (g *GitServer) URL(repo string) string { return g.srv.URL + "/" + repo + ".git" }

// RemoteDir is the bare remote of Repo.
func (g *GitServer) RemoteDir() string { return filepath.Join(g.root, Repo+".git") }

// SeenTokens lists the token of every request.
func (g *GitServer) SeenTokens() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.auth)
}

// RemoteSHA reads a branch of the bare remote, or "".
func (g *GitServer) RemoteSHA(branch string) string {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = g.RemoteDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// Seed makes a checkout in dir whose main holds one commit, pushes main
// to the remote, and leaves the checkout on a work branch. It returns the
// sha of main.
func (g *GitServer) Seed(t testing.TB, dir string) string {
	t.Helper()
	Git(t, dir, "init", "-q", "-b", "main")
	base := Commit(t, dir, "base", map[string]string{"README.md": "hello\n"})
	Git(t, dir, "push", "-q", g.RemoteDir(), "main")
	Git(t, dir, "checkout", "-q", "-b", "work")
	return base
}

// Git runs git for test setup with no user or system config.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_TERMINAL_PROMPT=0"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFile writes one file and its directories.
func WriteFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Commit writes files in the checkout and commits them. An empty content
// removes the file.
func Commit(t testing.TB, dir, msg string, files map[string]string) string {
	t.Helper()
	for p, c := range files {
		if c == "" {
			Git(t, dir, "rm", "-q", "--cached", "--ignore-unmatch", p)
			_ = os.Remove(filepath.Join(dir, p))
			continue
		}
		WriteFile(t, filepath.Join(dir, p), c)
		Git(t, dir, "add", "-f", p)
	}
	Git(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return Git(t, dir, "rev-parse", "HEAD")
}
