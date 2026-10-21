package rpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
	"github.com/iantanwx/loop/internal/secrets"
)

func newRedactor() *secrets.Redactor { return secrets.NewRedactor() }

// rawCall makes one gRPC call and returns the envelope, from the reply or
// from the status details, and the status.
func rawCall(t *testing.T, conn *grpc.ClientConn, method string, req *loopv1.VerbRequest, md ...string) (*loopv1.Envelope, *status.Status) {
	t.Helper()
	ctx := context.Background()
	if len(md) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, md...)
	}
	out := new(loopv1.Envelope)
	err := conn.Invoke(ctx, method, req, out)
	st := status.Convert(err)
	if err == nil {
		return out, st
	}
	for _, d := range st.Details() {
		if env, ok := d.(*loopv1.Envelope); ok {
			return env, st
		}
	}
	t.Fatalf("%s answered with no envelope: %v", method, err)
	return nil, nil
}
