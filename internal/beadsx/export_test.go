package beadsx

import "context"

// RunForTest runs one raw bd command in the workspace. Tests use it to put a
// workspace into a state the adapter never makes, so the guard can refuse it.
func (b *BD) RunForTest(ctx context.Context, args ...string) ([]byte, error) {
	return b.run(ctx, args...)
}

// HomeForTest is the isolated HOME of the client.
func (b *BD) HomeForTest() string { return b.cfg.Home }
