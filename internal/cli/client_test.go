package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
)

// fakeServer answers every call on an instance's socket with an ok
// envelope and records each call.
type fakeServer struct {
	mu    sync.Mutex
	calls []fakeCall
}

type fakeCall struct {
	method string
	token  string
	admin  string
}

func (f *fakeServer) seen() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// serveFake binds a fake server on the socket of instance.
func serveFake(t *testing.T, instance string) *fakeServer {
	t.Helper()
	sock := SocketPath(instance)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{}
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		req := new(loopv1.VerbRequest)
		if err := stream.RecvMsg(req); err != nil {
			return err
		}
		method, _ := grpc.MethodFromServerStream(stream)
		md, _ := metadata.FromIncomingContext(stream.Context())
		call := fakeCall{method: method, token: req.GetRunToken()}
		if v := md.Get(AdminCredentialHeader); len(v) > 0 {
			call.admin = v[0]
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		return stream.SendMsg(&loopv1.Envelope{Ok: true, Exit: int32(ExitDone), Verb: req.GetVerb()})
	}))
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	return f
}

// isolate keeps a test off the host's own instance: the default instance
// sits under a new home, and neither routing variable is set.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(InstanceEnv, "")
	t.Setenv(CheckoutExecEnv, "")
}

// newInstance is a new 0700 instance directory, resolved through
// symlinks so a path under it compares equal on every platform.
func newInstance(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "instance")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newCheckout writes a token file naming instance and binary into a new
// checkout.
func newCheckout(t *testing.T, instance, binary string) string {
	t.Helper()
	checkout := t.TempDir()
	tf := TokenFile{Token: "loopt_run", RunID: "run-1", Kind: PrincipalRun, Project: "alpha", Item: "T-1",
		Instance: instance, Binary: binary}
	if err := WriteTokenFile(checkout, tf); err != nil {
		t.Fatal(err)
	}
	return checkout
}

// writeBinary writes an executable file at path.
func writeBinary(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// execRecord records each exec the client asks for. The exec fails, as
// the real one does only on an error.
type execRecord struct {
	binary string
	argv   []string
	env    []string
	n      int
}

func (e *execRecord) exec(binary string, argv, env []string) error {
	e.n++
	e.binary, e.argv, e.env = binary, argv, env
	return errors.New("recorded exec")
}

// runFrom runs one command line from cwd through a client with no fixed
// instance, as the installed binary does.
func runFrom(t *testing.T, cwd string, ex *execRecord, args ...string) map[string]any {
	t.Helper()
	r := Surface()
	(&Client{Getwd: func() (string, error) { return cwd, nil }, Budget: time.Second, Exec: ex.exec}).Attach(r)
	var out, errOut bytes.Buffer
	r.Run(context.Background(), args, &out, &errOut)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("loop %v printed no envelope: %v\n%s\nstderr: %s", args, err, out.String(), errOut.String())
	}
	return env
}

func TestC8ARunCallReachesTheCheckoutsInstance(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, binary string
	}{
		{"no binary", ""},
		{"the binary is the running executable", self},
	} {
		for _, envJ := range []bool{false, true} {
			name := tc.name + ", LOOP_INSTANCE unset"
			if envJ {
				name = tc.name + ", LOOP_INSTANCE names another instance"
			}
			t.Run(name, func(t *testing.T) {
				i, j := newInstance(t), newInstance(t)
				atI, atJ := serveFake(t, i), serveFake(t, j)
				isolate(t)
				if envJ {
					t.Setenv(InstanceEnv, j)
				}
				ex := &execRecord{}
				env := runFrom(t, newCheckout(t, i, tc.binary), ex, "work", "show", "T-1")
				if env["ok"] != true {
					t.Fatalf("work show: %v", env)
				}
				if got := atI.seen(); len(got) != 1 || got[0].token != "loopt_run" {
					t.Fatalf("instance I saw %+v, want the one run call", got)
				}
				if got := atJ.seen(); len(got) != 0 {
					t.Fatalf("instance J saw %+v, want no call", got)
				}
				if ex.n != 0 {
					t.Fatalf("the client exec'd %s", ex.binary)
				}
			})
		}
	}
}

func TestC8ATokenWithNoInstanceKeepsTodaysRule(t *testing.T) {
	j := newInstance(t)
	atJ := serveFake(t, j)
	isolate(t)
	t.Setenv(InstanceEnv, j)
	env := runFrom(t, newCheckout(t, "", ""), &execRecord{}, "work", "show", "T-1")
	if env["ok"] != true || len(atJ.seen()) != 1 {
		t.Fatalf("work show: %v, J saw %+v", env, atJ.seen())
	}
}

func TestC9ABinaryOutsideTheInstanceNeverRuns(t *testing.T) {
	cases := map[string]func(t *testing.T, instance string) string{
		"an outside path": func(t *testing.T, _ string) string {
			return writeBinary(t, filepath.Join(t.TempDir(), "loop"))
		},
		"a .. path": func(t *testing.T, instance string) string {
			writeBinary(t, filepath.Join(filepath.Dir(instance), "outside", "loop"))
			return filepath.Join(instance, "..", "outside", "loop")
		},
		"an inside symlink to an outside target": func(t *testing.T, instance string) string {
			target := writeBinary(t, filepath.Join(t.TempDir(), "loop"))
			link := filepath.Join(instance, "current", "loop")
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
	}
	for name, binary := range cases {
		t.Run(name, func(t *testing.T) {
			i := newInstance(t)
			atI := serveFake(t, i)
			isolate(t)
			ex := &execRecord{}
			env := runFrom(t, newCheckout(t, i, binary(t, i)), ex, "work", "show", "T-1")
			if env["code"] != CodeUsage || env["rule"] != "binary_outside_instance" {
				t.Fatalf("want USAGE binary_outside_instance, got %v", env)
			}
			if ex.n != 0 {
				t.Fatalf("the client exec'd %s", ex.binary)
			}
			if got := atI.seen(); len(got) != 0 {
				t.Fatalf("the server saw %+v, want no call", got)
			}
		})
	}
}

func TestC10AHostAdminCallReadsNeitherField(t *testing.T) {
	i, j := newInstance(t), newInstance(t)
	atI, atJ := serveFake(t, i), serveFake(t, j)
	if err := os.WriteFile(AdminCredentialPath(j), []byte("admin-j\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	isolate(t)
	t.Setenv(InstanceEnv, j)
	// The token file sits above the working directory, and names another
	// instance and a binary outside it.
	checkout := newCheckout(t, i, writeBinary(t, filepath.Join(t.TempDir(), "loop")))
	cwd := filepath.Join(checkout, "sub")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	ex := &execRecord{}
	env := runFrom(t, cwd, ex, "doctor-github")
	if env["ok"] != true {
		t.Fatalf("doctor-github: %v", env)
	}
	if got := atJ.seen(); len(got) != 1 || got[0].admin != "admin-j" {
		t.Fatalf("instance J saw %+v, want the one host-admin call", got)
	}
	if got := atI.seen(); len(got) != 0 {
		t.Fatalf("instance I saw %+v, want no call", got)
	}
	if ex.n != 0 {
		t.Fatalf("the client exec'd %s", ex.binary)
	}
}

func TestC10TheExecHopRunsTheInstanceBinaryAtMostOnce(t *testing.T) {
	i := newInstance(t)
	atI := serveFake(t, i)
	isolate(t)
	// Two copies of one binary, each named by the token, would exec each
	// other on a check of the path alone.
	binary := writeBinary(t, filepath.Join(i, "versions", "v9.9.9", "loop"))
	link := filepath.Join(i, "current")
	if err := os.Symlink(filepath.Join("versions", "v9.9.9"), link); err != nil {
		t.Fatal(err)
	}
	checkout := newCheckout(t, i, filepath.Join(link, "loop"))

	first := &execRecord{}
	runFrom(t, checkout, first, "work", "show", "T-1")
	if first.n != 1 || first.binary != binary {
		t.Fatalf("exec %d times of %q, want once of %s", first.n, first.binary, binary)
	}
	if want := append([]string{binary}, os.Args[1:]...); !slices.Equal(first.argv, want) {
		t.Fatalf("argv %v, want %v", first.argv, want)
	}
	if !slices.Contains(first.env, CheckoutExecEnv+"=1") {
		t.Fatalf("the exec'd environment lacks %s=1", CheckoutExecEnv)
	}
	if got := atI.seen(); len(got) != 0 {
		t.Fatalf("the hopping process sent %+v", got)
	}

	// The exec'd process sees the variable, and sends the call itself.
	t.Setenv(CheckoutExecEnv, "1")
	second := &execRecord{}
	env := runFrom(t, checkout, second, "work", "show", "T-1")
	if second.n != 0 {
		t.Fatalf("the exec'd process exec'd %s again", second.binary)
	}
	if env["ok"] != true || len(atI.seen()) != 1 {
		t.Fatalf("work show: %v, I saw %+v", env, atI.seen())
	}
}
