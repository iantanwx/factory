package release

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// Error is a refusal with a registered code. The CLI prints Code, Rule and
// Hint in the envelope and exits by the code's row.
type Error struct {
	Code string
	Rule string
	Hint string
}

func (e *Error) Error() string { return e.Code + " " + e.Rule + ": " + e.Hint }

func refuse(code, rule, format string, args ...any) *Error {
	return &Error{Code: code, Rule: rule, Hint: fmt.Sprintf(format, args...)}
}

// Refusal codes this package adds to the closed set.
const (
	CodeSignatureInvalid   = "SIGNATURE_INVALID"
	CodeChecksumMismatch   = "CHECKSUM_MISMATCH"
	CodeVersionRefused     = "VERSION_REFUSED"
	CodeNoRollback         = "NO_ROLLBACK"
	CodeReleaseUnavailable = "RELEASE_UNAVAILABLE"
	CodeDrift              = "DRIFT"
	CodeDoctorFailed       = "DOCTOR_FAILED"
	CodeAppNotCreated      = "APP_NOT_CREATED"
	CodeMissingBinary      = "MISSING_BINARY"
)

// Refusals are the codes the host verbs add to the registry.
func Refusals() []cli.RefusalCode {
	return []cli.RefusalCode{
		{Code: CodeSignatureInvalid, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "the release's checksum file is not signed by a key this binary trusts"},
		{Code: CodeChecksumMismatch, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "a release asset, or a pinned bd or dolt, differs from its signed or embedded sha256"},
		{Code: CodeVersionRefused, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "the version rules refuse this release: a trusted-comment mismatch, a version below the highest installed, or a schema rule"},
		{Code: CodeNoRollback, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "the rollback would pass a release marked no-rollback, or an older binary cannot read the database; loop restore recovers the database but keeps the current release"},
		{Code: CodeReleaseUnavailable, Exit: cli.ExitWait, GRPC: codes.Unavailable, Retryable: true, Meaning: "the release could not be fetched; run the same command again later"},
		{Code: CodeDrift, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "the installed service, binaries or config differ from what loop install writes; the hint lists each difference"},
		{Code: CodeDoctorFailed, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "a loop doctor check failed; the hint lists each failed check"},
		{Code: CodeAppNotCreated, Exit: cli.ExitWait, GRPC: codes.Unavailable, Retryable: true, Meaning: "loop install waited, and no GitHub App was created; run loop install again"},
		{Code: CodeMissingBinary, Exit: cli.ExitRefused, GRPC: codes.FailedPrecondition, Meaning: "loop install found a required binary missing and wrote nothing; the hint names each one and how to install it"},
	}
}

// Server is what the host verbs need from a live server.
type Server interface {
	// Reachable makes one connection attempt and never waits for a
	// server that is down.
	Reachable(ctx context.Context) bool
	// Drain stops admission and waits for in-flight runs or the drain
	// timeout (AE6).
	Drain(ctx context.Context) (DrainReport, error)
}

// DrainReport is what a drain did.
type DrainReport struct {
	// Skipped says why no drain ran: the server was unreachable, or it
	// runs no scheduler, so it admits no run.
	Skipped  string `json:"skipped,omitempty"`
	Drained  bool   `json:"drained"`
	TimedOut bool   `json:"timed_out"`
	InFlight int    `json:"in_flight"`
}

// DefaultStopTimeout bounds the wait for a stopped server to release the
// instance lock.
const DefaultStopTimeout = 2 * time.Minute

// Host is what upgrade, rollback and restore share.
type Host struct {
	Layout  Layout
	Service ServiceManager
	Server  Server
	// StopTimeout is DefaultStopTimeout when zero.
	StopTimeout time.Duration
	// Now is time.Now when nil.
	Now func() time.Time
}

func (h Host) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// drain drains a reachable server and skips an unreachable one.
func (h Host) drain(ctx context.Context) (DrainReport, error) {
	if h.Server == nil || !h.Server.Reachable(ctx) {
		return DrainReport{Skipped: "the server is not reachable"}, nil
	}
	rep, err := h.Server.Drain(ctx)
	if err != nil {
		return rep, fmt.Errorf("release: drain: %w", err)
	}
	return rep, nil
}

// stop stops the service, then holds the instance lock, so no server
// writes the database until the caller releases it.
func (h Host) stop(ctx context.Context) (*rpc.InstanceLock, error) {
	if err := h.Service.Stop(ctx); err != nil {
		return nil, fmt.Errorf("release: stop the service: %w", err)
	}
	timeout := h.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	return h.Layout.lockInstance(ctx, timeout)
}

// start releases the lock and starts the service.
func (h Host) start(ctx context.Context, lock *rpc.InstanceLock) error {
	if err := lock.Release(); err != nil {
		return err
	}
	if err := h.Service.Start(ctx); err != nil {
		return fmt.Errorf("release: start the service: %w", err)
	}
	return nil
}

// abort restarts the service after a failed step, and joins any start
// failure to err.
func (h Host) abort(ctx context.Context, lock *rpc.InstanceLock, err error) error {
	if serr := h.start(ctx, lock); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}
