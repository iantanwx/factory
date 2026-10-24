package githubtest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The merge side of the world: labels, the merge API, GitHub's
// merge queue over GraphQL, merge-group branches, the contents API and
// branch refs. Nothing here models a vendor's wording: a refusal carries
// the status code, and a test asserts outcomes.

// BaseSHA is the tip of main in the fake.
var BaseSHA = strings.Repeat("b", 40)

// MergeCall is one merge request the fake received.
type MergeCall struct {
	PR     int
	SHA    string
	Method string
	Status int // the answer's status code
}

type groupRef struct{ ref, sha string }

type mergeWorld struct {
	queue     []int
	groups    []groupRef
	files     map[string]string // branch + "\x00" + path
	refs      map[string]string // branch -> sha
	merges    []MergeCall
	mutations []string
}

func nodeID(n int) string { return fmt.Sprintf("PR_%d", n) }

func labelsJSON(names []string) []any {
	out := []any{}
	for _, n := range names {
		out = append(out, map[string]any{"name": n})
	}
	return out
}

// MergeSHA is the merge commit the fake makes for PR n.
func MergeSHA(n int) string { return fmt.Sprintf("%040x", 0xe000+n) }

// ServeApp answers GET /app with the App's slug.
func (a *API) ServeApp(slug string) {
	a.Route("GET /app", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, 200, map[string]any{"id": AppID, "slug": slug, "owner": map[string]string{"login": Owner}})
	})
}

// Queue lists the PR numbers in GitHub's merge queue, in order.
func (w *World) Queue() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.merge.queue)
}

// Unqueue removes PR n from the merge queue, as GitHub does when a group
// fails or the PR merges.
func (w *World) Unqueue(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.merge.queue = slices.DeleteFunc(w.merge.queue, func(x int) bool { return x == n })
}

// AddGroup adds one merge-group branch, a name under
// refs/heads/gh-readonly-queue/, at sha.
func (w *World) AddGroup(branch, sha string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.merge.groups = append(w.merge.groups, groupRef{ref: "refs/heads/" + branch, sha: sha})
}

// SetFile puts one file on a branch for the contents API.
func (w *World) SetFile(branch, path, content string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.merge.files == nil {
		w.merge.files = map[string]string{}
	}
	w.merge.files[branch+"\x00"+path] = content
}

// File reads one file of a branch.
func (w *World) File(branch, path string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.merge.files[branch+"\x00"+path]
	return c, ok
}

// Merges lists every merge request, refused or not.
func (w *World) Merges() []MergeCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.merge.merges)
}

// Mutations lists every GraphQL mutation as "name #n".
func (w *World) Mutations() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.merge.mutations)
}

// Labels lists the labels of PR n.
func (w *World) Labels(n int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n < 1 || n > len(w.prs) {
		return nil
	}
	return slices.Clone(w.prs[n-1].Labels)
}

var (
	reLabels     = regexp.MustCompile(`^/repos/` + Repo + `/issues/(\d+)/labels$`)
	reLabel      = regexp.MustCompile(`^/repos/` + Repo + `/issues/(\d+)/labels/(.+)$`)
	reMerge      = regexp.MustCompile(`^/repos/` + Repo + `/pulls/(\d+)/merge$`)
	reMatching   = regexp.MustCompile(`^/repos/` + Repo + `/git/matching-refs/heads/(.*)$`)
	reRef        = regexp.MustCompile(`^/repos/` + Repo + `/git/ref/heads/(.+)$`)
	reContents   = regexp.MustCompile(`^/repos/` + Repo + `/contents/(.+)$`)
	mergeRefused = []string{"dirty", "behind", "blocked"}
)

func (w *World) pr(n string) *PR {
	i, _ := strconv.Atoi(n)
	if i < 1 || i > len(w.prs) {
		return nil
	}
	return w.prs[i-1]
}

// serveMerge answers the merge side. It runs under w.mu.
func (w *World) serveMerge(rw http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && reLabels.MatchString(path):
		p := w.pr(reLabels.FindStringSubmatch(path)[1])
		if p == nil {
			return false
		}
		WriteJSON(rw, 200, labelsJSON(p.Labels))
	case r.Method == http.MethodPost && reLabels.MatchString(path):
		p := w.pr(reLabels.FindStringSubmatch(path)[1])
		if p == nil {
			return false
		}
		var body struct {
			Labels []string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, l := range body.Labels {
			if !slices.Contains(p.Labels, l) {
				p.Labels = append(p.Labels, l)
			}
		}
		p.UpdatedAt = w.tick()
		WriteJSON(rw, 200, labelsJSON(p.Labels))
	case r.Method == http.MethodDelete && reLabel.MatchString(path):
		m := reLabel.FindStringSubmatch(path)
		p := w.pr(m[1])
		if p == nil || !slices.Contains(p.Labels, m[2]) {
			WriteJSON(rw, 404, map[string]string{"message": "Label does not exist"})
			return true
		}
		p.Labels = slices.DeleteFunc(p.Labels, func(l string) bool { return l == m[2] })
		p.UpdatedAt = w.tick()
		WriteJSON(rw, 200, labelsJSON(p.Labels))
	case r.Method == http.MethodPut && reMerge.MatchString(path):
		w.serveMergePR(rw, r, reMerge.FindStringSubmatch(path)[1])
	case r.Method == http.MethodPost && path == "/graphql":
		w.serveGraphQL(rw, r)
	case r.Method == http.MethodGet && reMatching.MatchString(path):
		prefix := "refs/heads/" + reMatching.FindStringSubmatch(path)[1]
		out := []any{}
		for _, g := range w.merge.groups {
			if strings.HasPrefix(g.ref, prefix) {
				out = append(out, map[string]any{"ref": g.ref, "object": map[string]any{"sha": g.sha, "type": "commit"}})
			}
		}
		WriteJSON(rw, 200, out)
	case r.Method == http.MethodGet && reRef.MatchString(path):
		b := reRef.FindStringSubmatch(path)[1]
		sha, ok := w.merge.refs[b]
		if !ok && b == "main" {
			sha, ok = BaseSHA, true
		}
		if !ok {
			WriteJSON(rw, 404, map[string]string{"message": "Not Found"})
			return true
		}
		WriteJSON(rw, 200, map[string]any{"ref": "refs/heads/" + b, "object": map[string]any{"sha": sha, "type": "commit"}})
	case r.Method == http.MethodPost && path == "/repos/"+Repo+"/git/refs":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		b := strings.TrimPrefix(body["ref"], "refs/heads/")
		if _, ok := w.merge.refs[b]; ok || b == "main" {
			WriteJSON(rw, 422, map[string]string{"message": "Reference already exists"})
			return true
		}
		if w.merge.refs == nil {
			w.merge.refs = map[string]string{}
		}
		w.merge.refs[b] = body["sha"]
		WriteJSON(rw, 201, map[string]any{"ref": body["ref"], "object": map[string]any{"sha": body["sha"]}})
	case r.Method == http.MethodGet && reContents.MatchString(path):
		p, ref := reContents.FindStringSubmatch(path)[1], r.URL.Query().Get("ref")
		c, ok := w.merge.files[ref+"\x00"+p]
		if !ok {
			WriteJSON(rw, 404, map[string]string{"message": "Not Found"})
			return true
		}
		WriteJSON(rw, 200, map[string]any{"path": p, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(c))})
	case r.Method == http.MethodPut && reContents.MatchString(path):
		p := reContents.FindStringSubmatch(path)[1]
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := body["branch"] + "\x00" + p
		if _, ok := w.merge.files[key]; ok && body["sha"] == "" {
			WriteJSON(rw, 422, map[string]string{"message": "sha wasn't supplied"})
			return true
		}
		raw, err := base64.StdEncoding.DecodeString(body["content"])
		if err != nil {
			WriteJSON(rw, 400, map[string]string{"message": "content is not base64"})
			return true
		}
		if w.merge.files == nil {
			w.merge.files = map[string]string{}
		}
		w.merge.files[key] = string(raw)
		WriteJSON(rw, 201, map[string]any{"content": map[string]any{"path": p}})
	default:
		return false
	}
	return true
}

func (w *World) serveMergePR(rw http.ResponseWriter, r *http.Request, n string) {
	p := w.pr(n)
	if p == nil {
		WriteJSON(rw, 404, map[string]string{"message": "Not Found"})
		return
	}
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	call := MergeCall{PR: p.Number, SHA: body["sha"], Method: body["merge_method"]}
	switch {
	case p.State != "open" || p.Merged:
		call.Status = 405
	case body["sha"] != p.HeadSHA:
		call.Status = 409
	case slices.Contains(mergeRefused, p.MergeableState):
		call.Status = 405
	default:
		call.Status = 200
	}
	w.merge.merges = append(w.merge.merges, call)
	if call.Status != 200 {
		WriteJSON(rw, call.Status, map[string]string{"message": "synthetic merge refusal"})
		return
	}
	p.Merged, p.State, p.MergeSHA = true, "closed", MergeSHA(p.Number)
	p.UpdatedAt = w.tick()
	WriteJSON(rw, 200, map[string]any{"sha": p.MergeSHA, "merged": true, "message": "merged"})
}

func (w *World) serveGraphQL(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	byNode := func() *PR {
		id, _ := body.Variables["pr"].(string)
		return w.pr(strings.TrimPrefix(id, "PR_"))
	}
	fail := func(msg string) {
		WriteJSON(rw, 200, map[string]any{"data": nil, "errors": []any{map[string]string{"message": msg}}})
	}
	switch {
	case strings.Contains(body.Query, "LoopQueueEntry"):
		num, _ := body.Variables["number"].(float64)
		p := w.pr(strconv.Itoa(int(num)))
		if p == nil {
			fail("Could not resolve to a PullRequest")
			return
		}
		var entry any
		if slices.Contains(w.merge.queue, p.Number) {
			entry = map[string]any{"state": "QUEUED"}
		}
		WriteJSON(rw, 200, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"id": nodeID(p.Number), "headRefOid": p.HeadSHA, "mergeQueueEntry": entry}}}})
	case strings.Contains(body.Query, "LoopReviewThreads"):
		num, _ := body.Variables["number"].(float64)
		// A thread is its root comment and each reply to it. GitHub keeps
		// a thread whose root comment is gone, and names the comments left.
		var roots []int64
		ids := map[int64][]any{}
		for _, c := range w.reviews[int(num)] {
			root, _ := c["in_reply_to_id"].(int64)
			if root == 0 {
				root = c["id"].(int64)
			}
			if _, ok := ids[root]; !ok {
				roots = append(roots, root)
			}
			ids[root] = append(ids[root], map[string]any{"databaseId": c["id"]})
		}
		nodes := []any{}
		for _, root := range roots {
			if t := w.threads[root]; !t.hidden {
				nodes = append(nodes, map[string]any{"isResolved": t.resolved, "isOutdated": t.outdated,
					"comments": map[string]any{"nodes": ids[root]}})
			}
		}
		WriteJSON(rw, 200, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"reviewThreads": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": nodes}}}}})
	case strings.Contains(body.Query, "LoopEnqueue"):
		p := byNode()
		head, _ := body.Variables["head"].(string)
		switch {
		case p == nil:
			fail("Could not resolve to a node")
		case head != p.HeadSHA:
			fail("the expected head does not match")
		default:
			if !slices.Contains(w.merge.queue, p.Number) {
				w.merge.queue = append(w.merge.queue, p.Number)
			}
			w.merge.mutations = append(w.merge.mutations, fmt.Sprintf("enqueue #%d", p.Number))
			WriteJSON(rw, 200, map[string]any{"data": map[string]any{"enqueuePullRequest": map[string]any{
				"mergeQueueEntry": map[string]any{"state": "QUEUED"}}}})
		}
	case strings.Contains(body.Query, "LoopDequeue"):
		p := byNode()
		if p == nil {
			fail("Could not resolve to a node")
			return
		}
		w.merge.queue = slices.DeleteFunc(w.merge.queue, func(x int) bool { return x == p.Number })
		w.merge.mutations = append(w.merge.mutations, fmt.Sprintf("dequeue #%d", p.Number))
		WriteJSON(rw, 200, map[string]any{"data": map[string]any{"dequeuePullRequest": map[string]any{"mergeQueueEntry": nil}}})
	default:
		fail("the fake knows no such document")
	}
}
