// Package github is the server's GitHub actor and reactor (U10). The server
// performs every GitHub act with its own App identity (R31): it pushes from
// a bare mirror, opens pull requests, posts statuses and comments, and it
// polls pull requests and CI with cursors in SQLite (KTD16). Agents hold no
// GitHub credential (R33), and the loop stores no copy of what GitHub holds
// (R32).
package github

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
)

// APIError is a non-2xx answer from GitHub. Message holds at most the
// first 400 bytes of the body.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: %s %s answered %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// Transient reports whether a retry can cure the answer: a 502, 503 or 504.
func (e *APIError) Transient() bool {
	return e.Status == http.StatusBadGateway || e.Status == http.StatusServiceUnavailable ||
		e.Status == http.StatusGatewayTimeout
}

// RateLimitError means the shared cooldown holds every call until Until
// (KTD16). No call went out.
type RateLimitError struct {
	Until time.Time
}

func (e *RateLimitError) Error() string {
	return "github: rate limited until " + e.Until.UTC().Format(time.RFC3339)
}

// UnavailableError is a network fault or a transient answer that outlived
// its retries. The result of a write that met it is unknown.
type UnavailableError struct {
	Err error
}

func (e *UnavailableError) Error() string { return "github: unavailable: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// SecretError refuses outbound text that holds a live secret (KTD27). Names
// lists the secrets by name, never by value.
type SecretError struct {
	Code  string // cli.CodeSecretInOutput or cli.CodeSecretInDiff
	Where string
	Names []string
}

func (e *SecretError) Error() string {
	return fmt.Sprintf("github: %s holds the value of %s", e.Where, strings.Join(e.Names, ", "))
}

// ScanError is a push the scan refused (KTD14).
type ScanError struct {
	Code   string // cli.CodeUnsafePath, cli.CodeSecretInDiff or cli.CodeSecretInOutput
	Rule   string
	Commit string
	Path   string
	Names  []string
	Reason string
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("github: commit %s: %s", short(e.Commit), e.Reason)
}

// RefusedError is a request the loop itself refuses before it reaches
// GitHub or git, for example a push of the base branch.
type RefusedError struct {
	Code string
	Rule string
	Hint string
}

func (e *RefusedError) Error() string { return "github: " + e.Hint }

// ErrNotConfigured means host config names no GitHub App.
var ErrNotConfigured = errors.New("github: host config names no GitHub App (credentials.github_app)")

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Refusal maps an error from this package to the CLI refusal a verb
// answers. A rate limit and an unavailable GitHub are exit 4, so the agent
// runs the same command again and its pending key joins the first attempt.
func Refusal(err error) cli.Refusal {
	var (
		rl  *RateLimitError
		ua  *UnavailableError
		api *APIError
		se  *SecretError
		sc  *ScanError
		rf  *RefusedError
	)
	switch {
	case errors.As(err, &rl):
		wait := math.Ceil(time.Until(rl.Until).Seconds())
		return cli.Refusal{Code: cli.CodeGitHubUnavailable, Rule: "rate_limited", RetryAfterSeconds: max(wait, 1),
			Hint: "GitHub rate-limited the loop's App; run the same command again after retry_after_seconds"}
	case errors.As(err, &ua):
		return cli.Refusal{Code: cli.CodeGitHubUnavailable, Rule: "github_unavailable", RetryAfterSeconds: 30,
			Hint: "GitHub did not answer; run the same command again"}
	case errors.As(err, &se):
		return cli.Refusal{Code: se.Code, Rule: "secret_in_text",
			Hint: fmt.Sprintf("%s holds the value of %s; remove it and run the command again", se.Where, strings.Join(se.Names, ", "))}
	case errors.As(err, &sc):
		return cli.Refusal{Code: sc.Code, Rule: sc.Rule, Hint: sc.Error()}
	case errors.As(err, &rf):
		return cli.Refusal{Code: rf.Code, Rule: rf.Rule, Hint: rf.Hint}
	case errors.Is(err, ErrNotConfigured):
		return cli.Refusal{Code: cli.CodeMissingSecret, Rule: "github_app_not_configured", Hint: err.Error()}
	case errors.As(err, &api):
		switch {
		case api.Transient():
			return cli.Refusal{Code: cli.CodeGitHubUnavailable, Rule: "github_unavailable", RetryAfterSeconds: 30, Hint: api.Error()}
		case api.Status == http.StatusNotFound:
			return cli.Refusal{Code: cli.CodeNotFound, Rule: "github_not_found", Hint: api.Error()}
		case api.Status >= 400 && api.Status < 500:
			return cli.Refusal{Code: cli.CodeGitHubRefused, Rule: "github_refused", Hint: api.Error()}
		}
	}
	return cli.Refusal{Code: cli.CodeInternal, Rule: "internal", Hint: err.Error()}
}

// Refuse is Refusal as a result.
func Refuse(err error) cli.Result { return cli.Refuse(Refusal(err)) }
