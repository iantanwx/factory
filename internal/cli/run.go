package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// New returns the registry with the whole CLI surface and the verbs this
// unit implements: schema and version.
func New() *Registry {
	r := NewRegistry()
	for _, v := range surface {
		if err := r.RegisterVerb(v); err != nil {
			panic(err)
		}
	}
	must(r.SetHandler("schema", r.schemaVerb))
	must(r.SetHandler("version", versionVerb))
	return r
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// Run answers one command line and returns the process exit code. It
// prints exactly one envelope on stdout. stderr carries only a failure to
// write stdout.
func (r *Registry) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	requestID := os.Getenv(RequestIDEnv)
	if !validRequestID(requestID) {
		requestID = newRequestID()
	}
	c, res := r.parse(args)
	if res == nil {
		answer := r.dispatch(ctx, c, requestID)
		res = &answer
	}
	env := r.seal(c.path, requestID, *res)
	if err := writeEnvelope(stdout, env, c.text); err != nil {
		_, _ = fmt.Fprintf(stderr, "loop: write output: %v\n", err)
		return int(ExitUndecided)
	}
	return int(env.Exit)
}

func (r *Registry) dispatch(ctx context.Context, c call, requestID string) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = *internal(fmt.Errorf("%s panicked: %v", c.path, p))
		}
	}()
	h := c.verb.handler
	if h == nil && c.verb.Side == SideServer {
		h = r.remote
	}
	if h == nil {
		h = notImplemented
	}
	return h(ctx, Invocation{Verb: c.verb, Args: c.args, Flags: c.flags, RequestID: requestID})
}

func notImplemented(_ context.Context, inv Invocation) Result {
	return Refuse(Refusal{
		Code: CodeNotImplemented, Rule: "not_implemented",
		Hint: fmt.Sprintf("loop %s defines %s but does not implement it yet", Version, inv.Verb.Path),
	})
}

func internal(err error) *Result {
	res := Refuse(Refusal{Code: CodeInternal, Rule: "internal", Hint: err.Error()})
	return &res
}

func (r *Registry) schemaVerb(context.Context, Invocation) Result {
	s, err := r.Schema()
	if err != nil {
		return *internal(err)
	}
	return Found(s)
}

func versionVerb(context.Context, Invocation) Result {
	return Found(map[string]string{
		"version":  Version,
		"go":       runtime.Version(),
		"platform": runtime.GOOS + "/" + runtime.GOARCH,
	})
}

// validRequestID accepts a coordinator's own request id: 1 to 128
// characters of letters, digits, '-', '_', '.' and ':'.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)
		if !ok {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
