package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	// Exec replaces the process with the checkout's loop binary.
	// syscall.Exec when nil.
	Exec func(binary string, argv, env []string) error
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
			return nil, new(Internal(err))
		}
		cc.instance = dir
	}
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
		return nil, new(Internal(fmt.Errorf("read the working directory: %w", err)))
	}
	cc.cwd = cwd
	checkout, err := FindCheckout(cwd)
	switch {
	case errors.Is(err, ErrNoCheckout):
	case err != nil:
		return nil, new(Internal(err))
	default:
		tf, err := ReadTokenFile(checkout)
		if err != nil {
			return nil, new(Internal(err))
		}
		cc.token, cc.checkout = &tf, checkout
	}
	// The token file routes a run's own call, and that call sends no
	// host-admin credential. A host verb is a host-admin call. It ignores
	// the file, so the credential never reaches another instance.
	switch {
	case cc.token != nil && !isHostVerb(inv.Verb):
		if res := c.route(cc); res != nil {
			return nil, res
		}
	case slices.Contains(inv.Verb.Callers, "host"):
		// The credential comes only from the instance's file. A missing
		// file sends none, and the server refuses the verb.
		if cred, err := ReadAdminCredential(cc.instance); err == nil {
			cc.admin = cred
		}
	}
	cc.socket = SocketPath(cc.instance)
	return cc, nil
}

// isHostVerb reports whether every caller of v is a host label, the rule
// by which DeriveMatrix names a host verb.
func isHostVerb(v *Verb) bool {
	return slices.Contains(v.Callers, "host") && !slices.ContainsFunc(v.Callers, func(c string) bool {
		return !slices.Contains(hostVerbLabels, c)
	})
}

// route follows the checkout's token file. A binary other than this one
// runs in its place, at most once and only from under the instance. Then
// the call goes to the instance the file names.
func (c *Client) route(cc *callCtx) *Result {
	tf := cc.token
	if tf.Binary != "" && os.Getenv(CheckoutExecEnv) == "" {
		bin, err := filepath.EvalSymlinks(tf.Binary)
		if err != nil {
			return new(Internal(fmt.Errorf("resolve the checkout's loop binary: %w", err)))
		}
		self, err := os.Executable()
		if err == nil {
			self, err = filepath.EvalSymlinks(self)
		}
		if err != nil {
			return new(Internal(fmt.Errorf("resolve this loop binary: %w", err)))
		}
		if bin != self {
			if !within(bin, tf.Instance) {
				return usage("binary_outside_instance", fmt.Sprintf(
					"the token file names the binary %s, which resolves outside the instance %s; the CLI runs no binary from outside its instance",
					tf.Binary, tf.Instance))
			}
			return new(Internal(c.exec(bin)))
		}
	}
	if tf.Instance != "" {
		cc.instance = tf.Instance
	}
	return nil
}

// within reports whether the resolved path lies under dir, after dir is
// resolved through symlinks too.
func within(path, dir string) bool {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// exec replaces the process with bin, with the same arguments and
// environment, and CheckoutExecEnv set. It returns only on a failure.
func (c *Client) exec(bin string) error {
	execve := c.Exec
	if execve == nil {
		execve = syscall.Exec
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, CheckoutExecEnv+"=") })
	err := execve(bin, append([]string{bin}, os.Args[1:]...), append(env, CheckoutExecEnv+"=1"))
	return fmt.Errorf("run %s: %w", bin, err)
}

func (cc *callCtx) request(inv Invocation) *loopv1.VerbRequest {
	req := &loopv1.VerbRequest{
		Verb:          inv.Verb.Path,
		Args:          append([]string{}, inv.Args...),
		Flags:         map[string]string{},
		RequestId:     inv.RequestID,
		ClientVersion: Version,
	}
	maps.Copy(req.Flags, inv.Flags)
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
			return Internal(fmt.Errorf("read stdin: %w", err))
		}
		req.Flags[ExtraStdin] = base64.StdEncoding.EncodeToString(value)
		clear(value)
	}

	if pushesHead(inv.Verb.Path) && cc.checkout != "" {
		head, captures, err := capturesOf(ctx, inv, cc.checkout, cc.cwd)
		if err != nil {
			return *usage("head_unreadable", err.Error(), inv.Verb.Path)
		}
		req.Flags[ExtraHead] = head
		if captures != "" {
			req.Flags[ExtraCaptures] = captures
		}
	}

	if path := inv.Flags["file"]; path != "" {
		data, err := readFileFlag(path, cc.cwd)
		if err != nil {
			return *usage("file_unreadable", err.Error(), inv.Verb.Path)
		}
		req.Flags[ExtraFile] = base64.StdEncoding.EncodeToString(data)
	}
	if dir := inv.Flags["evidence"]; dir != "" {
		files, err := ReadEvidence(dir, cc.cwd)
		if err != nil {
			return *usage("evidence_unreadable", err.Error(), inv.Verb.Path)
		}
		req.Flags[ExtraEvidence] = EncodeEvidence(files)
	}
	if inv.Verb.Path == "inbox wait" {
		// The server holds the call until shortly before this deadline,
		// then answers exit 4.
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
	if exit := exitOf(res); pending != nil && (exit == ExitDone || exit == ExitNothing || exit == ExitRefused) {
		// A definitive answer, or a NOTHING that wrote nothing: the next
		// same command is a new write.
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
		return nil, "", new(Internal(fmt.Errorf("read the pending log: %w", err)))
	}
	for _, e := range entries {
		if e.Verb == entry.Verb && e.ArgsHash == entry.ArgsHash && e.ContentHash == entry.ContentHash {
			if e.ExpectedStatus != "" {
				req.Flags[ExtraExpectedStatus] = e.ExpectedStatus
			}
			return pending, e.Key, nil
		}
	}
	item, project := inv.Arg("item"), inv.Flags["project"]
	if p, epic, ok := signoffEpic(inv); ok {
		item, project = epic, p
	}
	if item == "" {
		item = cc.token.Item
	}
	var st ItemState
	if item != "" {
		reader := c.State
		if reader == nil {
			reader = serverState{c: c, cc: cc, project: project}
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
		return nil, "", new(Internal(fmt.Errorf("record the pending write: %w", err)))
	}
	return pending, key, nil
}

// signoffEpic is the project and the epic of an answer to a sign-off
// entry, signoff/<project>/<epic>. The PRD and the spec of the epic share
// the entry, so the answer's key holds the epic's state: the same words
// at the next sign-off are a new write (factorio-ny3n).
func signoffEpic(inv Invocation) (project, epic string, ok bool) {
	parts := strings.Split(inv.Arg("entry"), "/")
	if inv.Verb.Path != "inbox answer" || len(parts) != 3 || parts[0] != "signoff" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// serverState reads `work show <item>` over the same socket. Project is
// the write's --project: the coordinator's token holds no project, so its
// read names the one its write names.
type serverState struct {
	c       *Client
	cc      *callCtx
	project string
}

func (s serverState) ItemState(ctx context.Context, item string) (ItemState, *Result) {
	req := &loopv1.VerbRequest{
		Verb: "work show", Args: []string{item}, Flags: map[string]string{},
		RequestId: newRequestID(), ClientVersion: Version,
	}
	if s.project != "" {
		req.Flags["project"] = s.project
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
			return Internal(fmt.Errorf("the call was cancelled: %w", ctx.Err()))
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
	return Internal(fmt.Errorf("the server answered without an envelope: %s: %s", st.Code(), st.Message())), nil
}
