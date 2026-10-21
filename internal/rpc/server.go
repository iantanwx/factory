// Package rpc is the server side of the CLI's socket (U4): the gRPC server
// that answers every verb, the version handshake, run tokens, the
// capability matrix, peer checks and idempotent replay.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// maxKeyLen bounds an idempotency key.
const maxKeyLen = 128

// osGetuid is the server's uid. Tests replace it.
var osGetuid = os.Getuid

// Call is one verified call that reaches a handler.
type Call struct {
	Inv       cli.Invocation
	Principal Principal
	Request   *loopv1.VerbRequest
}

// Handler answers one server verb. The server seals, redacts and logs the
// result. ctx also carries the principal: PrincipalFrom(ctx).
type Handler func(ctx context.Context, call *Call) cli.Result

// Options configure a Server.
type Options struct {
	// Registry is the CLI surface with its capability matrix: cli.Surface().
	Registry *cli.Registry
	Store    *store.Store
	Redactor *secrets.Redactor
	Tokens   *Tokens
	// Instance is the instance directory. It holds the host-admin
	// credential file.
	Instance string
	// Version is this server's release; cli.Version when empty.
	Version string
	// PreviousVersion is the release this one replaced. A run started under
	// it may keep calling (KTD13). Empty admits no other version.
	PreviousVersion string
	// Logger receives one line for each call. The server wraps it in the
	// redactor.
	Logger *slog.Logger
	// HoldsItem checks a run's or a room's item. TokenItem when nil.
	HoldsItem HoldsItem
	// Checkouts lists the registered checkouts and agent sessions.
	// Tokens.LiveCheckouts when nil.
	Checkouts CheckoutLister
	// Proc reads a caller's working directory. DefaultProc when nil.
	Proc ProcInspector
	// OnCall sees each authorized call of a principal with a run id,
	// before its handler runs. The scheduler (U8) records the run's last
	// CLI call with it (KTD7, KTD8). Optional.
	OnCall func(ctx context.Context, p Principal)
}

// Server answers every RPC through one generic handler.
type Server struct {
	opts   Options
	reg    *cli.Registry
	matrix *cli.CapabilityMatrix
	log    *slog.Logger

	mu       sync.RWMutex
	handlers map[string]Handler

	keyMu sync.Mutex
	keys  map[string]*keyLock

	grpc *grpc.Server
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

// New builds a server. It refuses a registry without a capability matrix,
// because access is denied by default and the matrix is the only grant.
func New(opts Options) (*Server, error) {
	if opts.Registry == nil || opts.Store == nil || opts.Tokens == nil {
		return nil, errors.New("rpc: a server needs a registry, a store and tokens")
	}
	m, ok := opts.Registry.CapabilityMatrix()
	if !ok {
		return nil, errors.New("rpc: the registry has no capability matrix")
	}
	if opts.Version == "" {
		opts.Version = cli.Version
	}
	if opts.Redactor == nil {
		opts.Redactor = secrets.NewRedactor()
	}
	if opts.HoldsItem == nil {
		opts.HoldsItem = TokenItem
	}
	if opts.Checkouts == nil {
		opts.Checkouts = opts.Tokens.LiveCheckouts
	}
	if opts.Proc == nil {
		opts.Proc = DefaultProc
	}
	next := slog.Default().Handler()
	if opts.Logger != nil {
		next = opts.Logger.Handler()
	}
	s := &Server{
		opts:     opts,
		reg:      opts.Registry,
		matrix:   m,
		log:      NewRedactingLogger(next, opts.Redactor),
		handlers: map[string]Handler{},
		keys:     map[string]*keyLock{},
	}
	s.grpc = grpc.NewServer(
		grpc.Creds(newPeerCreds()),
		grpc.UnknownServiceHandler(s.stream),
	)
	return s, nil
}

// Handle sets the handler of one server verb by its path, for example
// "work peek". A server verb with no handler answers NOT_IMPLEMENTED.
func (s *Server) Handle(path string, h Handler) error {
	v, ok := s.reg.Verb(path)
	if !ok {
		return fmt.Errorf("rpc: no verb %q", path)
	}
	if v.Side != cli.SideServer {
		return fmt.Errorf("rpc: %q is a client verb", path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.handlers[path]; dup {
		return fmt.Errorf("rpc: verb %q already has a handler", path)
	}
	s.handlers[path] = h
	return nil
}

// Logger is the server's redacting logger.
func (s *Server) Logger() *slog.Logger { return s.log }

// Redactor is the server's redactor.
func (s *Server) Redactor() *secrets.Redactor { return s.opts.Redactor }

// Serve answers calls on l until Stop or GracefulStop.
func (s *Server) Serve(l net.Listener) error { return s.grpc.Serve(l) }

// GracefulStop stops accepting and waits for every in-flight call. When
// ctx ends first, it stops at once.
func (s *Server) GracefulStop(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.grpc.Stop()
		<-done
	}
}

// Stop closes every connection at once.
func (s *Server) Stop() { s.grpc.Stop() }

// stream is the one gRPC handler. Every RPC is VerbRequest to Envelope.
func (s *Server) stream(_ any, stream grpc.ServerStream) error {
	method, _ := grpc.MethodFromServerStream(stream)
	req := new(loopv1.VerbRequest)
	if err := stream.RecvMsg(req); err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "rpc: the client sent no request")
		}
		return err
	}
	env, replayed := s.Answer(stream.Context(), method, req)
	if replayed {
		stream.SetTrailer(metadata.Pairs(cli.ReplayedTrailer, "true"))
	}
	pe, err := cli.EnvelopeToProto(env)
	if err != nil {
		env = s.reg.Seal(env.Verb, env.RequestID, cli.Result{ServerVersion: s.opts.Version,
			Refusal: &cli.Refusal{Code: cli.CodeInternal, Rule: "encode", Hint: "the server could not encode its answer"}})
		pe, _ = cli.EnvelopeToProto(env)
	}
	if env.OK {
		return stream.SendMsg(pe)
	}
	st := status.New(s.reg.GRPCStatus(env.Code), env.Code+": "+env.Hint)
	if withEnv, err := st.WithDetails(pe); err == nil {
		st = withEnv
	}
	return st.Err()
}

// Answer decides one call and returns the sealed, redacted envelope, and
// whether it is a replay. It logs one line.
func (s *Server) Answer(ctx context.Context, method string, req *loopv1.VerbRequest) (env cli.Envelope, replayed bool) {
	start := time.Now()
	path, res, p, replayedEnv := s.decide(ctx, method, req)
	if replayedEnv != nil {
		env, replayed = *replayedEnv, true
	} else {
		res.ServerVersion = s.opts.Version
		env = s.reg.Seal(path, req.GetRequestId(), res)
	}
	env = s.redact(env)
	attrs := []any{
		"verb", env.Verb, "request_id", env.RequestID, "actor", p.Actor(), "exit", int(env.Exit),
		"replayed", replayed, "ms", time.Since(start).Milliseconds(),
	}
	if p.CwdUnverified {
		attrs = append(attrs, "cwd_unverified", true)
	}
	if env.Refusal != nil {
		attrs = append(attrs, "code", env.Code, "rule", env.Rule, "hint", env.Hint)
	}
	s.log.InfoContext(ctx, "call", attrs...)
	return env, replayed
}

// decide runs the checks in order and then the handler. It returns the
// verb's path for the envelope, and a stored envelope on a replay.
func (s *Server) decide(ctx context.Context, method string, req *loopv1.VerbRequest) (string, cli.Result, Principal, *cli.Envelope) {
	p := Principal{Kind: cli.PrincipalNone}
	path := req.GetVerb()

	peerInfo, ok := peerFrom(ctx)
	if !ok {
		return path, *denied("no_peer", "", "the server could not identify the caller's process"), p, nil
	}
	if err := CheckPeerUID(peerInfo, osGetuid()); err != nil {
		return path, *denied("foreign_uid", "", err.Error()), p, nil
	}
	p.Peer = peerInfo

	probe := method == cli.AuthorizeMethod && req.GetFlags()[cli.ExtraAuthorize] == "true"
	var v *cli.Verb
	if probe {
		v, ok = s.reg.Verb(path)
		if ok && (v.Side != cli.SideClient || !s.matrix.IsHostVerb(v.Path)) {
			ok = false
		}
	} else {
		v, ok = s.reg.VerbByRPC(method)
		if ok && v.Path != path {
			return v.Path, usageResult("verb_mismatch", fmt.Sprintf("the RPC %s carries the verb %q", method, path)), p, nil
		}
	}
	if !ok {
		if res := s.checkVersion(req.GetClientVersion(), p); res != nil {
			return path, *res, p, nil
		}
		return path, usageResult("unknown_verb", fmt.Sprintf("this server has no verb %q at %s", path, method)), p, nil
	}
	path = v.Path

	p, fault := s.resolve(ctx, req, p)
	if res := s.checkVersion(req.GetClientVersion(), p); res != nil {
		return path, *res, p, nil
	}
	if fault != nil {
		return path, *fault, p, nil
	}
	inv, bad := s.reg.Validate(path, req.GetArgs(), req.GetFlags(), req.GetRequestId())
	if bad != nil {
		return path, *bad, p, nil
	}
	if res := s.authorize(ctx, inv, &p); res != nil {
		return path, *res, p, nil
	}
	if probe {
		return path, cli.Found(map[string]any{"authorized": true, "verb": path}), p, nil
	}

	if s.opts.OnCall != nil && p.RunID != "" {
		s.opts.OnCall(ctx, p)
	}
	call := &Call{Inv: inv, Principal: p, Request: req}
	ctx = withPrincipal(ctx, p)
	key := req.GetIdempotencyKey()
	if !v.Write || key == "" || p.RunID == "" {
		return path, s.run(ctx, call), p, nil
	}
	if len(key) > maxKeyLen {
		return path, usageResult("bad_idempotency_key", "the idempotency key is too long"), p, nil
	}
	res, replay := s.idempotent(ctx, call, key)
	return path, res, p, replay
}

// idempotent replays a stored answer for key, or runs the call and stores
// its answer when the answer is definitive (R18, KTD13).
func (s *Server) idempotent(ctx context.Context, call *Call, key string) (cli.Result, *cli.Envelope) {
	unlock := s.lockKey(key)
	defer unlock()
	inv := call.Inv
	fp := store.Fingerprint{
		RunID:       call.Principal.RunID,
		Verb:        inv.Verb.Path,
		ArgsHash:    cli.ArgsHash(inv.Verb.Path, inv.Args, inv.Flags),
		ContentHash: inv.Extra[cli.ExtraContentHash],
	}
	var (
		rec   store.IdemRecord
		found bool
	)
	err := s.opts.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		rec, found, err = tx.LookupKey(key, fp)
		return err
	})
	if errors.Is(err, store.ErrKeyConflict) {
		return usageResult("idempotency_key_conflict", "this idempotency key was sent before with other arguments or content"), nil
	}
	if err != nil {
		return *internalResult(err), nil
	}
	if found && !rec.Pending() {
		env, err := cli.EnvelopeFromJSON(rec.Result)
		if err != nil {
			return *internalResult(fmt.Errorf("read the stored answer: %w", err)), nil
		}
		env.RequestID = call.Request.GetRequestId()
		env.ServerVersion = s.opts.Version
		env.Replayed = true
		return cli.Result{}, &env
	}

	res := s.run(ctx, call)
	res.ServerVersion = s.opts.Version
	env := s.redact(s.reg.Seal(inv.Verb.Path, call.Request.GetRequestId(), res))
	if env.Exit == cli.ExitDone || env.Exit == cli.ExitNothing || env.Exit == cli.ExitRefused {
		raw, err := json.Marshal(env)
		if err == nil {
			// The answer is stored even when the caller went away: its
			// retry must find it.
			err = s.opts.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
				return tx.StoreResult(store.IdemRecord{Key: key, Fingerprint: fp, Result: raw})
			})
		}
		if err != nil {
			s.log.ErrorContext(ctx, "store the idempotent answer", "verb", inv.Verb.Path, "err", err)
		}
	}
	return res, nil
}

func (s *Server) lockKey(key string) func() {
	s.keyMu.Lock()
	l, ok := s.keys[key]
	if !ok {
		l = &keyLock{}
		s.keys[key] = l
	}
	l.refs++
	s.keyMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.keyMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.keys, key)
		}
		s.keyMu.Unlock()
	}
}

// run calls the verb's handler, or answers NOT_IMPLEMENTED.
func (s *Server) run(ctx context.Context, call *Call) (res cli.Result) {
	s.mu.RLock()
	h := s.handlers[call.Inv.Verb.Path]
	s.mu.RUnlock()
	if h == nil {
		return cli.Refuse(cli.Refusal{
			Code: cli.CodeNotImplemented, Rule: "not_implemented",
			Hint: fmt.Sprintf("server %s defines %s but does not implement it yet", s.opts.Version, call.Inv.Verb.Path),
		})
	}
	defer func() {
		if r := recover(); r != nil {
			res = *internalResult(fmt.Errorf("%s panicked: %v", call.Inv.Verb.Path, r))
		}
	}()
	return h(ctx, call)
}

// resolve finds the principal: a run token wins, then the host-admin
// credential, then nobody.
func (s *Server) resolve(ctx context.Context, req *loopv1.VerbRequest, p Principal) (Principal, *cli.Result) {
	if token := req.GetRunToken(); token != "" {
		tp, err := s.opts.Tokens.Verify(ctx, token)
		switch {
		case errors.Is(err, ErrTokenRevoked):
			return p, denied("token_revoked", "", "this run token's run settled; a settled run calls nothing")
		case errors.Is(err, ErrTokenUnknown):
			return p, denied("token_unknown", "", "the server issued no such run token")
		case err != nil:
			return p, internalResult(err)
		}
		tp.Peer = p.Peer
		return tp, nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if vals := md.Get(cli.AdminCredentialHeader); len(vals) > 0 && vals[0] != "" {
		if !adminMatches(s.opts.Instance, vals[0]) {
			return p, denied("admin_credential_invalid", "", "the host-admin credential does not match the instance's credential file")
		}
		p.Kind = cli.PrincipalHost
	}
	return p, nil
}

// redact passes every string of an envelope through the redactor (KTD27).
func (s *Server) redact(env cli.Envelope) cli.Envelope {
	raw, err := json.Marshal(env)
	if err != nil {
		return env
	}
	clean := s.opts.Redactor.Redact(string(raw))
	if clean == string(raw) {
		return env
	}
	out, err := cli.EnvelopeFromJSON([]byte(clean))
	if err == nil {
		return out
	}
	// The redaction broke the JSON: keep the envelope's shape and drop
	// every free-text field.
	env.Data = "[REDACTED]"
	if env.Refusal != nil {
		r := *env.Refusal
		r.Hint = s.opts.Redactor.Redact(r.Hint)
		r.Item = s.opts.Redactor.Redact(r.Item)
		env.Refusal = &r
	}
	return env
}

func usageResult(rule, hint string) cli.Result {
	return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: rule, Hint: hint})
}
