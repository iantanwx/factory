package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
)

// Wire names shared by the client and the server.
const (
	// AdminCredentialHeader carries the host-admin credential of a host
	// verb in the call's metadata, never in the VerbRequest.
	AdminCredentialHeader = "loop-admin-credential"
	// ReplayedTrailer is "true" when the server answered with the stored
	// result of an idempotency key.
	ReplayedTrailer = "loop-replayed"
	// AuthorizeMethod is the RPC that a client-side host verb calls to have
	// the server check the caller before the verb runs. The request names
	// the client verb and sets ExtraAuthorize.
	AuthorizeMethod = "/loop.v1.MetaService/Status"
	// ExtraAuthorize marks an authorization probe.
	ExtraAuthorize = ReservedPrefix + "authorize"
	// RequestIDEnv is the coordinator's own durable request id. Its session
	// outlives any run, so it keeps the id and sends it again on a retry.
	RequestIDEnv = "LOOP_REQUEST_ID"
)

// maxStdin bounds what secret set reads: the secret store's value limit
// plus a trailing CR LF and one byte to detect an oversize value.
const maxStdin = 64<<10 + 3

// gatedHostVerbs are the client-side host verbs whose caller the server
// checks before the verb runs.
var gatedHostVerbs = []string{"upgrade", "rollback"}

// ItemState is what a write's key needs from the item: its status and its
// applied-transition sequence.
type ItemState struct {
	Status string
	Seq    int64
}

// StateReader reads an item's state for a key. The default reads
// `work show <item>` from the server and takes data.status and data.seq.
type StateReader interface {
	ItemState(ctx context.Context, item string) (ItemState, *Result)
}

// Client sends server verbs over the instance's socket.
type Client struct {
	// Instance is the instance directory. InstanceDir() when empty.
	Instance string
	// Stdin is where secret set reads its value.
	Stdin io.Reader
	// Getwd returns the working directory. os.Getwd when nil.
	Getwd func() (string, error)
	// Budget is the wait budget. effectiveBudget() when zero.
	Budget time.Duration
	// State reads the expected status and sequence of a write's item.
	State StateReader
}

// Attach makes c the remote of every server verb in r, and gates the
// client-side host verbs that act on a live server.
func (c *Client) Attach(r *Registry) {
	r.SetRemote(c.Remote)
	for _, path := range gatedHostVerbs {
		must(r.SetHandler(path, c.gate(path)))
	}
}

// call is the state of one invocation on its way to the server.
type callCtx struct {
	instance string
	socket   string
	deadline time.Time
	budget   time.Duration
	token    *TokenFile
	checkout string
	cwd      string
	admin    string
}

func (c *Client) prepare(inv Invocation) (*callCtx, *Result) {
	cc := &callCtx{instance: c.Instance}
	if cc.instance == "" {
		dir, err := InstanceDir()
		if err != nil {
			return nil, internal(err)
		}
		cc.instance = dir
	}
	cc.socket = SocketPath(cc.instance)
	cc.budget = c.Budget
	if cc.budget <= 0 {
		cc.budget = effectiveBudget()
	}
	cc.deadline = time.Now().Add(cc.budget)
	getwd := c.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	cwd, err := getwd()
	if err != nil {
		return nil, internal(fmt.Errorf("read the working directory: %w", err))
	}
	cc.cwd = cwd
	checkout, err := FindCheckout(cwd)
	switch {
	case errors.Is(err, ErrNoCheckout):
	case err != nil:
		return nil, internal(err)
	default:
		tf, err := ReadTokenFile(checkout)
		if err != nil {
			return nil, internal(err)
		}
		cc.token, cc.checkout = &tf, checkout
	}
	if slices.Contains(inv.Verb.Callers, "host") {
		// The credential comes only from the instance's file. A missing
		// file sends none, and the server refuses the verb.
		if cred, err := ReadAdminCredential(cc.instance); err == nil {
			cc.admin = cred
		}
	}
	return cc, nil
}

func (cc *callCtx) request(inv Invocation) *loopv1.VerbRequest {
	req := &loopv1.VerbRequest{
		Verb:          inv.Verb.Path,
		Args:          append([]string{}, inv.Args...),
		Flags:         map[string]string{},
		RequestId:     inv.RequestID,
		ClientVersion: Version,
	}
	for k, v := range inv.Flags {
		req.Flags[k] = v
	}
	if cc.token != nil {
		req.RunToken = cc.token.Token
	}
	return req
}

// Remote sends one server verb and returns the server's answer.
func (c *Client) Remote(ctx context.Context, inv Invocation) Result {
	cc, fault := c.prepare(inv)
	if fault != nil {
		return *fault
	}
	req := cc.request(inv)
	if inv.Verb.Path == "secret set" {
		if c.Stdin == nil {
			return *usage("no_stdin", "secret set reads the value from stdin; pipe it in", "secret set")
		}
		value, err := io.ReadAll(io.LimitReader(c.Stdin, maxStdin))
		if err != nil {
			return *internal(fmt.Errorf("read stdin: %w", err))
		}
		req.Flags[ExtraStdin] = base64.StdEncoding.EncodeToString(value)
		clear(value)
	}

	if inv.Verb.Path == "branch push" && cc.checkout != "" {
		head, err := CheckoutHead(ctx, cc.checkout)
		if err != nil {
			return *usage("head_unreadable", err.Error(), inv.Verb.Path)
		}
		req.Flags[ExtraHead] = head
	}

	if path := inv.Flags["file"]; path != "" {
		data, err := readFileFlag(path, cc.cwd)
		if err != nil {
			return *usage("file_unreadable", err.Error(), inv.Verb.Path)
		}
		req.Flags[ExtraFile] = base64.StdEncoding.EncodeToString(data)
	}
	if inv.Verb.Path == "inbox wait" {
		// The server holds the call until shortly before this deadline,
		// then answers exit 4 (U12).
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, cc.deadline)
		defer cancel()
	}

	var pending *Pending
	if inv.Verb.Write && cc.token != nil {
		p, key, res := c.reserveKey(ctx, cc, inv, req)
		if res != nil {
			return *res
		}
		pending = p
		req.IdempotencyKey = key
	}

	res := cc.send(ctx, inv.Verb.RPC, req)
	if exit := exitOf(res); pending != nil && (exit == ExitDone || exit == ExitRefused) {
		// A definitive answer: the next same command is a new write.
		_ = pending.Clear(req.IdempotencyKey)
	}
	return res
}

// readFileFlag reads the file a --file flag names, relative to the
// caller's working directory, up to MaxFileBytes.
func readFileFlag(path, cwd string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read --file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read --file: %w", err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("--file %s is larger than %d bytes", path, MaxFileBytes)
	}
	return data, nil
}

// exitOf is the exit a result seals to.
func exitOf(res Result) ExitCode {
	if res.Refusal == nil {
		return res.Exit
	}
	if code, ok := lookupBaseRefusal(res.Refusal.Code); ok {
		return code.Exit
	}
	return ExitUndecided
}

func lookupBaseRefusal(code string) (RefusalCode, bool) {
	for _, c := range baseRefusals {
		if c.Code == code {
			return c, true
		}
	}
	return RefusalCode{}, false
}

// reserveKey finds or derives the write's key and records it as pending
// before the send.
func (c *Client) reserveKey(ctx context.Context, cc *callCtx, inv Invocation, req *loopv1.VerbRequest) (*Pending, string, *Result) {
	content, err := ContentHash(ctx, inv, cc.checkout, cc.cwd)
	if err != nil {
		return nil, "", usage("content_unreadable", err.Error(), inv.Verb.Path)
	}
	req.Flags[ExtraContentHash] = content
	entry := PendingEntry{
		Verb: inv.Verb.Path, Args: req.Args, Flags: inv.Flags,
		ArgsHash: ArgsHash(inv.Verb.Path, inv.Args, inv.Flags), ContentHash: content,
		Created: time.Now().UTC(),
	}
	pending := OpenPending(cc.checkout)
	entries, err := pending.Entries()
	if err != nil {
		return nil, "", internal(fmt.Errorf("read the pending log: %w", err))
	}
	for _, e := range entries {
		if e.Verb == entry.Verb && e.ArgsHash == entry.ArgsHash && e.ContentHash == entry.ContentHash {
			if e.ExpectedStatus != "" {
				req.Flags[ExtraExpectedStatus] = e.ExpectedStatus
			}
			return pending, e.Key, nil
		}
	}
	item := inv.Arg("item")
	if item == "" {
		item = cc.token.Item
	}
	var st ItemState
	if item != "" {
		reader := c.State
		if reader == nil {
			reader = serverState{c: c, cc: cc}
		}
		var res *Result
		if st, res = reader.ItemState(ctx, item); res != nil {
			return nil, "", res
		}
	}
	parts := KeyParts{
		RunID: cc.token.RunID, Verb: inv.Verb.Path, Args: inv.Args, Flags: inv.Flags,
		ExpectedStatus: st.Status, Seq: st.Seq, ContentHash: content,
		RequestID: os.Getenv(RequestIDEnv),
	}
	entry.ExpectedStatus = st.Status
	key, reused, err := pending.Reserve(entry, func() (string, error) { return DeriveKey(parts), nil })
	if err == nil && !reused && st.Status != "" {
		req.Flags[ExtraExpectedStatus] = st.Status
	}
	if err != nil {
		return nil, "", internal(fmt.Errorf("record the pending write: %w", err))
	}
	return pending, key, nil
}

// serverState reads `work show <item>` over the same socket.
type serverState struct {
	c  *Client
	cc *callCtx
}

func (s serverState) ItemState(ctx context.Context, item string) (ItemState, *Result) {
	req := &loopv1.VerbRequest{
		Verb: "work show", Args: []string{item}, Flags: map[string]string{},
		RequestId: newRequestID(), ClientVersion: Version,
	}
	if s.cc.token != nil {
		req.RunToken = s.cc.token.Token
	}
	res := s.cc.send(ctx, "/loop.v1.WorkService/Show", req)
	if res.Refusal != nil {
		switch res.Refusal.Code {
		case CodeNotImplemented, CodeNotFound:
			// No item state yet: the key holds an empty status.
			return ItemState{}, nil
		}
		return ItemState{}, &res
	}
	data, _ := res.Data.(map[string]any)
	st := ItemState{}
	st.Status, _ = data["status"].(string)
	if seq, ok := data["seq"].(float64); ok {
		st.Seq = int64(seq)
	}
	return st, nil
}

// gate returns the handler of a client-side host verb: the server checks
// the caller first, and the verb runs only when the server allows it.
func (c *Client) gate(path string) Handler {
	return func(ctx context.Context, inv Invocation) Result {
		cc, fault := c.prepare(inv)
		if fault != nil {
			return *fault
		}
		req := cc.request(inv)
		req.Flags[ExtraAuthorize] = "true"
		if res := cc.send(ctx, AuthorizeMethod, req); res.Refusal != nil {
			return res
		}
		return notImplemented(ctx, inv)
	}
}

// send makes one call, and waits through an unreachable server until the
// deadline.
func (cc *callCtx) send(ctx context.Context, method string, req *loopv1.VerbRequest) Result {
	if cc.admin != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, AdminCredentialHeader, cc.admin)
	}
	var b backoff
	var last error
	for {
		res, err := cc.attempt(ctx, method, req)
		if err == nil {
			return res
		}
		last = err
		if ctx.Err() != nil {
			return *internal(fmt.Errorf("the call was cancelled: %w", ctx.Err()))
		}
		if !sleepUntil(ctx, b.wait(), cc.deadline) {
			return unavailable(cc.socket, cc.budget, last)
		}
	}
}

// attempt makes one call. A nil error means the server answered with an
// envelope. An error means the server was not reachable or not ready.
func (cc *callCtx) attempt(ctx context.Context, method string, req *loopv1.VerbRequest) (Result, error) {
	conn, err := grpc.NewClient("unix://"+cc.socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()
	out := new(loopv1.Envelope)
	var trailer metadata.MD
	err = conn.Invoke(ctx, method, req, out, grpc.Trailer(&trailer))
	replayed := len(trailer.Get(ReplayedTrailer)) > 0 && trailer.Get(ReplayedTrailer)[0] == "true"
	if err == nil {
		return ResultFromProto(out, replayed), nil
	}
	st := status.Convert(err)
	for _, d := range st.Details() {
		if env, ok := d.(*loopv1.Envelope); ok {
			return ResultFromProto(env, replayed), nil
		}
	}
	if st.Code() == codes.Unavailable || st.Code() == codes.Canceled && ctx.Err() == nil {
		return Result{}, err
	}
	return *internal(fmt.Errorf("the server answered without an envelope: %s: %s", st.Code(), st.Message())), nil
}
