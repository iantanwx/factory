package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
)

// DefaultAPI is GitHub's REST endpoint.
const DefaultAPI = "https://api.github.com"

// maxBody bounds what one answer may hold. A diff or a log excerpt larger
// than this is cut by its own reader first.
const maxBody = 16 << 20

// retryDelays are the waits before each retry of a read. Two entries, so
// one read makes at most three attempts. Ported from RETRY_DELAYS_MS in
// scripts/sdlc/github-retry.ts (alpha).
var retryDelays = []time.Duration{time.Second, 3 * time.Second}

// Client makes REST calls to GitHub. Every call passes the shared
// cooldown. A read retries a transient answer. A write never repeats,
// because a 504 can arrive after GitHub did the write. Every write body
// passes the redactor first (KTD27).
type Client struct {
	Base     string
	HTTP     *http.Client
	Cooldown *Cooldown
	Redactor *secrets.Redactor
	// Sleep waits between retries. A test replaces it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Call is one REST request.
type Call struct {
	Method string
	Path   string // starts with "/"; may hold a query
	Body   any    // JSON-encoded when not nil
	Token  string // "Bearer" credential: a JWT or an installation token
	Accept string // application/vnd.github+json when empty
	// Repeatable lets a non-GET call retry, for example a token mint: a
	// second mint makes a second token, and the first expires unused.
	Repeatable bool
	// Raw keeps the body as text instead of decoding JSON into out.
	Raw *string
}

// Response is the part of an answer a caller reads beside the body.
type Response struct {
	Status int
	Header http.Header
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do sends c and decodes a JSON answer into out when out is not nil.
func (cl *Client) Do(ctx context.Context, c Call, out any) (*Response, error) {
	var body []byte
	if c.Body != nil {
		raw, err := json.Marshal(c.Body)
		if err != nil {
			return nil, fmt.Errorf("github: encode %s %s: %w", c.Method, c.Path, err)
		}
		if cl.Redactor != nil {
			// The check reads the decoded strings too: JSON escapes a quote
			// or a backslash, and a value can hold either.
			if hit, names := cl.Redactor.Find(string(raw)); hit {
				return nil, &SecretError{Code: cli.CodeSecretInOutput, Where: "the GitHub write", Names: names}
			}
			if hit, names := findInJSON(cl.Redactor, c.Body); hit {
				return nil, &SecretError{Code: cli.CodeSecretInOutput, Where: "the GitHub write", Names: names}
			}
		}
		body = raw
	}
	repeat := c.Method == http.MethodGet || c.Repeatable
	sleep := cl.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for n := 0; ; n++ {
		resp, err := cl.once(ctx, c, body, out)
		if err == nil {
			return resp, nil
		}
		if !repeat || n >= len(retryDelays) || !transient(err) {
			return resp, err
		}
		if serr := sleep(ctx, retryDelays[n]); serr != nil {
			return resp, err
		}
	}
}

func transient(err error) bool {
	var api *APIError
	if errors.As(err, &api) {
		return api.Transient()
	}
	var ua *UnavailableError
	return errors.As(err, &ua)
}

func (cl *Client) once(ctx context.Context, c Call, body []byte, out any) (*Response, error) {
	if err := cl.Cooldown.Check(); err != nil {
		return nil, err
	}
	base := cl.Base
	if base == "" {
		base = DefaultAPI
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, strings.TrimRight(base, "/")+c.Path, rd)
	if err != nil {
		return nil, fmt.Errorf("github: build %s %s: %w", c.Method, c.Path, err)
	}
	accept := c.Accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "loop-server")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := cl.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The error text names the URL and never a header.
		return nil, &UnavailableError{Err: fmt.Errorf("%s %s: %w", c.Method, c.Path, err)}
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, &UnavailableError{Err: fmt.Errorf("%s %s: read the answer: %w", c.Method, c.Path, err)}
	}
	resp := &Response{Status: res.StatusCode, Header: res.Header}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		text := string(data)
		if rl := cl.Cooldown.Observe(res.StatusCode, res.Header, text); rl != nil {
			return resp, rl
		}
		if len(text) > 400 {
			text = text[:400]
		}
		return resp, &APIError{Method: c.Method, Path: c.Path, Status: res.StatusCode, Message: strings.TrimSpace(text)}
	}
	switch {
	case c.Raw != nil:
		*c.Raw = string(data)
	case out != nil && len(bytes.TrimSpace(data)) > 0:
		if err := json.Unmarshal(data, out); err != nil {
			// An answer the loop cannot read is unknown, never success.
			return resp, &UnavailableError{Err: fmt.Errorf("%s %s: decode the answer: %w", c.Method, c.Path, err)}
		}
	}
	return resp, nil
}

// findInJSON checks every string inside v after a JSON round trip, so a
// value that JSON escaping changed is still found.
func findInJSON(r *secrets.Redactor, v any) (bool, []string) {
	raw, err := json.Marshal(v)
	if err != nil {
		return false, nil
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return false, nil
	}
	var names []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			if hit, n := r.Find(t); hit {
				names = append(names, n...)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(generic)
	return len(names) > 0, names
}

// nextPage reports whether a Link header names a next page.
func nextPage(h http.Header) bool {
	return strings.Contains(h.Get("Link"), `rel="next"`)
}
