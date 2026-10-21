package beadsx

import (
	"errors"
	"fmt"
	"strings"
)

// ExitCASMiss is the exit code bd 1.3.1 returns when every failure is a stale
// --if-status or --if-assignee guard. Nothing was written.
const ExitCASMiss = 13

// Error kinds. Test them with errors.Is.
var (
	// ErrCASMiss means the guard of a conditional update no longer held, and
	// nothing was written. A retry with the same guard is pointless.
	ErrCASMiss = errors.New("beadsx: compare-and-set miss")
	// ErrNotFound means no issue has the id.
	ErrNotFound = errors.New("beadsx: issue not found")
	// ErrUnavailable means the call did not reach a healthy Dolt server, or
	// its result is unknown (a timeout or a dropped connection). It is never
	// a miss. The caller retries, and it re-reads the bead before it decides
	// whether a write was applied.
	ErrUnavailable = errors.New("beadsx: beads unavailable")
	// ErrVersion means a binary is not the pinned version.
	ErrVersion = errors.New("beadsx: binary is not the pinned version")
	// ErrRefused means the workspace guard refused the workspace.
	ErrRefused = errors.New("beadsx: workspace refused")
	// ErrMalformed means bd wrote output the adapter cannot parse.
	ErrMalformed = errors.New("beadsx: malformed bd output")
	// ErrInvalid means bd or the fake rejected an argument.
	ErrInvalid = errors.New("beadsx: invalid request")
	// ErrJournalTruncated means the journal no longer holds the records after
	// the requested seq. The consumer recovers through reconcile.
	ErrJournalTruncated = errors.New("beadsx: events journal truncated")
	// ErrFailed is any other bd failure.
	ErrFailed = errors.New("beadsx: bd failed")
)

// BDError is one failed bd call. Kind is one of the Err values above, and
// Cause, when set, is the underlying error such as context.DeadlineExceeded.
type BDError struct {
	Op      string // the bd subcommand, such as "update"
	Exit    int    // -1 when the process did not exit normally
	Message string // bd's error text, or the transport error
	Kind    error
	Cause   error
}

func (e *BDError) Error() string {
	return fmt.Sprintf("%v: bd %s (exit %d): %s", e.Kind, e.Op, e.Exit, e.Message)
}

// Unwrap returns the kind and the cause, so errors.Is matches either one.
func (e *BDError) Unwrap() []error {
	if e.Cause != nil {
		return []error{e.Kind, e.Cause}
	}
	return []error{e.Kind}
}

// unavailableMarks are bd error texts that mean the Dolt server is not
// reachable, or that a connection broke during the call.
var unavailableMarks = []string{
	"dolt server unreachable",
	"failed to open database",
	"connection refused",
	"broken pipe",
	"connection reset",
	"bad connection",
	"invalid connection",
	"unexpected eof",
	"i/o timeout",
	"server has gone away",
	"lost connection",
}

// notFoundMarks are bd error texts that mean an unknown issue id.
var notFoundMarks = []string{
	"no issue found matching",
	"no issues found matching",
}

// classify maps one finished bd call to an error kind. A zero exit is no
// error. Exit 13 is always a miss. Every other exit reads bd's message.
func classify(exit int, message string) error {
	if exit == 0 {
		return nil
	}
	if exit == ExitCASMiss {
		return ErrCASMiss
	}
	m := strings.ToLower(message)
	for _, s := range unavailableMarks {
		if strings.Contains(m, s) {
			return ErrUnavailable
		}
	}
	for _, s := range notFoundMarks {
		if strings.Contains(m, s) {
			return ErrNotFound
		}
	}
	switch {
	case strings.Contains(m, "events_journal_truncated"):
		return ErrJournalTruncated
	case strings.Contains(m, "validation failed"), strings.Contains(m, "invalid "),
		strings.Contains(m, "unknown flag"), strings.Contains(m, "cannot combine"),
		strings.Contains(m, "requires types.custom"), strings.Contains(m, "is not a valid"):
		return ErrInvalid
	}
	return ErrFailed
}
