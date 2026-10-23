package cli

import (
	"testing"

	"google.golang.org/grpc/codes"
)

// TestRefusalCodeTable pins every code to one exit code, one gRPC status
// and one retryable flag. A code added to the seed without a row here
// fails, and so does a row whose code was removed.
func TestRefusalCodeTable(t *testing.T) {
	want := map[string]struct {
		exit      ExitCode
		grpc      codes.Code
		retryable bool
	}{
		CodeUsage:                {2, codes.InvalidArgument, false},
		CodeNotFound:             {2, codes.NotFound, false},
		CodeInternal:             {2, codes.Internal, false},
		CodeNotImplemented:       {2, codes.Unimplemented, false},
		CodeVersionMismatch:      {3, codes.FailedPrecondition, false},
		CodeCapabilityDenied:     {3, codes.PermissionDenied, false},
		CodeTransitionNotAllowed: {3, codes.FailedPrecondition, false},
		CodeStatusChanged:        {3, codes.Aborted, false},
		CodeLeaseNotHeld:         {3, codes.Aborted, false},
		CodeRetryLimit:           {3, codes.ResourceExhausted, false},
		CodeCapFull:              {3, codes.ResourceExhausted, false},
		CodeMutexHeld:            {3, codes.ResourceExhausted, false},
		CodeDependencyOpen:       {3, codes.FailedPrecondition, false},
		CodeMissingSecret:        {3, codes.FailedPrecondition, false},
		CodeConfigConflict:       {3, codes.FailedPrecondition, false},
		CodeSecretInDiff:         {3, codes.FailedPrecondition, false},
		CodeSecretInOutput:       {3, codes.FailedPrecondition, false},
		CodePRDRequired:          {3, codes.FailedPrecondition, false},
		CodeDraining:             {4, codes.Unavailable, true},
		CodeServerUnavailable:    {4, codes.Unavailable, true},
		CodeAlreadyExists:        {3, codes.AlreadyExists, false},
		CodeProjectBusy:          {3, codes.FailedPrecondition, false},
		CodeLeaseHeld:            {3, codes.FailedPrecondition, false},
		CodeBeadsUnavailable:     {4, codes.Unavailable, true},
		CodeUnsafePath:           {3, codes.FailedPrecondition, false},
		CodeRemoteAhead:          {3, codes.FailedPrecondition, false},
		CodeGitHubUnavailable:    {4, codes.Unavailable, true},
		CodeGitHubRefused:        {3, codes.FailedPrecondition, false},
		CodeRoutedToLoop:         {3, codes.FailedPrecondition, false},
		CodeNoOperatorStatement:  {3, codes.FailedPrecondition, false},
		CodeGatesFailed:          {3, codes.FailedPrecondition, false},
		CodeStoryUnknown:         {3, codes.FailedPrecondition, false},
	}
	r := New()
	got := r.RefusalCodes()
	if len(got) != len(want) {
		t.Errorf("registry holds %d codes, table holds %d", len(got), len(want))
	}
	for _, c := range got {
		w, ok := want[c.Code]
		if !ok {
			t.Errorf("code %s has no row in this table", c.Code)
			continue
		}
		if c.Exit != w.exit || c.GRPC != w.grpc || c.Retryable != w.retryable {
			t.Errorf("code %s maps to exit %d %v retryable %v; want exit %d %v retryable %v",
				c.Code, c.Exit, c.GRPC, c.Retryable, w.exit, w.grpc, w.retryable)
		}
		if r.GRPCStatus(c.Code) != w.grpc {
			t.Errorf("GRPCStatus(%s) = %v, want %v", c.Code, r.GRPCStatus(c.Code), w.grpc)
		}
	}
	for code := range want {
		if _, ok := r.Refusal(code); !ok {
			t.Errorf("table row %s is not registered", code)
		}
	}
}

func TestRegisterRefusalRefusesAnIncompleteMapping(t *testing.T) {
	cases := map[string]RefusalCode{
		"no mapping":     {Code: "NEW_CODE", Meaning: "m"},
		"exit 0":         {Code: "NEW_CODE", Exit: ExitDone, GRPC: codes.Aborted, Meaning: "m"},
		"exit 1":         {Code: "NEW_CODE", Exit: ExitNothing, GRPC: codes.Aborted, Meaning: "m"},
		"exit 5":         {Code: "NEW_CODE", Exit: 5, GRPC: codes.Aborted, Meaning: "m"},
		"grpc OK":        {Code: "NEW_CODE", Exit: ExitRefused, GRPC: codes.OK, Meaning: "m"},
		"grpc unknown":   {Code: "NEW_CODE", Exit: ExitRefused, GRPC: codes.Code(99), Meaning: "m"},
		"no meaning":     {Code: "NEW_CODE", Exit: ExitRefused, GRPC: codes.Aborted},
		"lower case":     {Code: "new_code", Exit: ExitRefused, GRPC: codes.Aborted, Meaning: "m"},
		"duplicate code": {Code: CodeUsage, Exit: ExitUndecided, GRPC: codes.InvalidArgument, Meaning: "m"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := NewRegistry().RegisterRefusal(c); err == nil {
				t.Fatalf("RegisterRefusal(%+v) succeeded; want an error", c)
			}
		})
	}
	ok := RefusalCode{Code: "NEW_CODE", Exit: ExitRefused, GRPC: codes.Aborted, Meaning: "m"}
	r := NewRegistry()
	if err := r.RegisterRefusal(ok); err != nil {
		t.Fatalf("a complete mapping must register: %v", err)
	}
	if got := r.GRPCStatus("NEW_CODE"); got != codes.Aborted {
		t.Fatalf("GRPCStatus = %v", got)
	}
}

func TestExitCodeTable(t *testing.T) {
	got := ExitCodes()
	if len(got) != 5 {
		t.Fatalf("want exit codes 0..4, got %d rows", len(got))
	}
	for i, e := range got {
		if int(e.Exit) != i || e.Name == "" || e.Meaning == "" {
			t.Errorf("row %d is %+v", i, e)
		}
	}
}
