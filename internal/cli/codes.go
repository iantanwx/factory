package cli

import (
	"fmt"
	"regexp"

	"google.golang.org/grpc/codes"
)

// ExitCode is the process exit status. Every verb shares one table.
type ExitCode int

const (
	ExitDone      ExitCode = 0 // done, or work found
	ExitNothing   ExitCode = 1 // nothing to do
	ExitUndecided ExitCode = 2 // could not decide
	ExitRefused   ExitCode = 3 // refused by the table or the compare-and-set
	ExitWait      ExitCode = 4 // keep waiting
)

// ExitInfo describes one row of the exit-code table.
type ExitInfo struct {
	Exit    ExitCode `json:"exit"`
	Name    string   `json:"name"`
	Meaning string   `json:"meaning"`
}

var exitTable = []ExitInfo{
	{ExitDone, "DONE", "done, or work found"},
	{ExitNothing, "NOTHING", "nothing to do; data is []"},
	{ExitUndecided, "UNDECIDED", "could not decide; the refusal code says why"},
	{ExitRefused, "REFUSED", "refused by the transition table or the compare-and-set"},
	{ExitWait, "WAIT", "keep waiting, then call again"},
}

// ExitCodes returns the exit-code table in exit order.
func ExitCodes() []ExitInfo { return append([]ExitInfo(nil), exitTable...) }

// The core refusal codes. Other packages add codes with RegisterRefusal.
const (
	CodeUsage                = "USAGE"
	CodeNotFound             = "NOT_FOUND"
	CodeInternal             = "INTERNAL"
	CodeNotImplemented       = "NOT_IMPLEMENTED"
	CodeVersionMismatch      = "VERSION_MISMATCH"
	CodeCapabilityDenied     = "CAPABILITY_DENIED"
	CodeTransitionNotAllowed = "TRANSITION_NOT_ALLOWED"
	CodeStatusChanged        = "STATUS_CHANGED"
	CodeLeaseNotHeld         = "LEASE_NOT_HELD"
	CodeRetryLimit           = "RETRY_LIMIT"
	CodeCapFull              = "CAP_FULL"
	CodeMutexHeld            = "MUTEX_HELD"
	CodeDependencyOpen       = "DEPENDENCY_OPEN"
	CodeMissingSecret        = "MISSING_SECRET"
	CodeConfigConflict       = "CONFIG_CONFLICT"
	CodeSecretInDiff         = "SECRET_IN_DIFF"
	CodeSecretInOutput       = "SECRET_IN_OUTPUT"
	CodePRDRequired          = "PRD_REQUIRED"
	CodeDraining             = "DRAINING"
	CodeServerUnavailable    = "SERVER_UNAVAILABLE"
	CodeAlreadyExists        = "ALREADY_EXISTS"
	CodeProjectBusy          = "PROJECT_BUSY"
	CodeLeaseHeld            = "LEASE_HELD"
	CodeBeadsUnavailable     = "BEADS_UNAVAILABLE"
	CodeUnsafePath           = "UNSAFE_PATH"
	CodeRemoteAhead          = "REMOTE_AHEAD"
	CodeGitHubUnavailable    = "GITHUB_UNAVAILABLE"
	CodeGitHubRefused        = "GITHUB_REFUSED"
	CodeRoutedToLoop         = "ROUTED_TO_LOOP"
	CodeNoOperatorStatement  = "NO_OPERATOR_STATEMENT"
	CodeGatesFailed          = "GATES_FAILED"
	CodeStoryUnknown         = "STORY_UNKNOWN"
)

// RefusalCode maps one refusal code to exactly one exit code and one gRPC
// status. Retryable says whether the same call may succeed later unchanged.
type RefusalCode struct {
	Code      string
	Exit      ExitCode
	GRPC      codes.Code
	Retryable bool
	Meaning   string
}

var baseRefusals = []RefusalCode{
	{CodeUsage, ExitUndecided, codes.InvalidArgument, false, "the call does not match the verb's schema"},
	{CodeNotFound, ExitUndecided, codes.NotFound, false, "the named item does not exist"},
	{CodeInternal, ExitUndecided, codes.Internal, false, "the loop failed; the hint holds the detail"},
	{CodeNotImplemented, ExitUndecided, codes.Unimplemented, false, "this binary defines the verb but does not implement it yet"},
	{CodeVersionMismatch, ExitRefused, codes.FailedPrecondition, false, "the CLI and the server run different versions"},
	{CodeCapabilityDenied, ExitRefused, codes.PermissionDenied, false, "the caller's token does not allow this verb on this item"},
	{CodeTransitionNotAllowed, ExitRefused, codes.FailedPrecondition, false, "the transition table has no edge for this move"},
	{CodeStatusChanged, ExitRefused, codes.Aborted, false, "the item's status changed since the caller read it"},
	{CodeLeaseNotHeld, ExitRefused, codes.Aborted, false, "the caller does not hold the item's current lease"},
	{CodeRetryLimit, ExitRefused, codes.ResourceExhausted, false, "two attempts failed with unchanged inputs"},
	{CodeCapFull, ExitRefused, codes.ResourceExhausted, false, "every run slot is taken"},
	{CodeMutexHeld, ExitRefused, codes.ResourceExhausted, false, "another run holds the stage mutex"},
	{CodeDependencyOpen, ExitRefused, codes.FailedPrecondition, false, "a dependency of the item is not done"},
	{CodeMissingSecret, ExitRefused, codes.FailedPrecondition, false, "a secret the verb needs is not in the store"},
	{CodeConfigConflict, ExitRefused, codes.FailedPrecondition, false, "an injected path collides with a tracked path"},
	{CodeSecretInDiff, ExitRefused, codes.FailedPrecondition, false, "the pushed content holds a secret or an injected path"},
	{CodeSecretInOutput, ExitRefused, codes.FailedPrecondition, false, "the outbound text holds a secret"},
	{CodePRDRequired, ExitRefused, codes.FailedPrecondition, false, "the work needs a merged PRD first"},
	{CodeDraining, ExitWait, codes.Unavailable, true, "the server is draining before an upgrade"},
	{CodeServerUnavailable, ExitWait, codes.Unavailable, true, "the CLI cannot reach the server"},
	{CodeAlreadyExists, ExitRefused, codes.AlreadyExists, false, "the named thing is already registered"},
	{CodeProjectBusy, ExitRefused, codes.FailedPrecondition, false, "the project has a live run"},
	{CodeLeaseHeld, ExitRefused, codes.FailedPrecondition, false, "another run holds the item's lease"},
	{CodeBeadsUnavailable, ExitWait, codes.Unavailable, true, "the server recorded the write and cannot reach beads yet; run the same command again"},
	{CodeUnsafePath, ExitRefused, codes.FailedPrecondition, false, "the pushed content changes .gitmodules, a gitlink, or a filter or LFS line in .gitattributes; the operator makes that change by hand"},
	{CodeRemoteAhead, ExitRefused, codes.FailedPrecondition, false, "the remote branch holds commits that the pushed head lacks; sync the branch first"},
	{CodeGitHubUnavailable, ExitWait, codes.Unavailable, true, "GitHub did not answer or rate-limited the App; run the same command again after retry_after_seconds"},
	{CodeGitHubRefused, ExitRefused, codes.FailedPrecondition, false, "GitHub refused the request; the hint holds GitHub's answer"},
	{CodeRoutedToLoop, ExitRefused, codes.FailedPrecondition, false, "the triage rubric routes the finding back to the loop; the hint names the next step, and the item keeps its state"},
	{CodeNoOperatorStatement, ExitRefused, codes.FailedPrecondition, false, "the room's decision file cites no operator statement; a room applies only what a person decided"},
	{CodeGatesFailed, ExitRefused, codes.FailedPrecondition, false, "a gate of loop gates failed; the hint names it, and stderr holds each finding"},
	{CodeStoryUnknown, ExitRefused, codes.FailedPrecondition, false, "a ticket cites no story label, or a label its epic's merged PRD does not hold; the whole batch is refused"},
}

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)

// validate refuses a code without a full mapping. A refusal never exits 0
// or 1 and never maps to gRPC OK.
func (c RefusalCode) validate() error {
	switch {
	case !codePattern.MatchString(c.Code):
		return fmt.Errorf("refusal code %q is not UPPER_SNAKE_CASE", c.Code)
	case c.Exit < ExitUndecided || c.Exit > ExitWait:
		return fmt.Errorf("refusal code %s maps to exit %d; want 2, 3 or 4", c.Code, c.Exit)
	case c.GRPC == codes.OK || c.GRPC > codes.Unauthenticated:
		return fmt.Errorf("refusal code %s maps to gRPC %v; want a non-OK status", c.Code, c.GRPC)
	case c.Meaning == "":
		return fmt.Errorf("refusal code %s has no meaning", c.Code)
	}
	return nil
}
