package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

const testPlatform = "linux/amd64"

// releaseWorld serves signed releases from a local server.
type releaseWorld struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte // "/<version>/<asset>", and "/skills/<name>" for a skill archive
	pub   PublicKey
	sec   SecretKey
	// thirdParty is the third-party.json that publish serves; nil serves a
	// release that pins no skill.
	thirdParty []byte
}

func newReleaseWorld(t *testing.T) *releaseWorld {
	t.Helper()
	w := &releaseWorld{t: t, files: map[string][]byte{}}
	w.pub, w.sec = mustKey(t)
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		data, ok := w.files[r.URL.Path]
		w.mu.Unlock()
		if !ok {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(data)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *releaseWorld) source() Source { return Source{Base: w.srv.URL, HTTP: w.srv.Client()} }

// fetcher reads each skill archive from /skills/<name>.
func (w *releaseWorld) fetcher() SkillFetcher {
	return SkillFetcher{HTTP: w.srv.Client(), URL: func(p pack.ThirdParty) string { return w.srv.URL + "/skills/" + p.Name }}
}

// publish signs and serves a release whose linux/amd64 binary is bin.
// signer and trusted override the key and the trusted comment.
func (w *releaseWorld) publish(meta Metadata, bin []byte, signer *SecretKey, trusted string) {
	w.t.Helper()
	metaBytes := meta.Encode()
	pins := w.thirdParty
	if pins == nil {
		pins = []byte(`{"skills": []}` + "\n")
	}
	sums := Checksums{AssetName("linux", "amd64"): hexid.SHA256(bin), MetadataFile: hexid.SHA256(metaBytes), ThirdPartyAsset: hexid.SHA256(pins)}
	if signer == nil {
		signer = &w.sec
	}
	if trusted == "" {
		trusted = TrustedComment(meta.Version)
	}
	sig, err := Sign(*signer, sums.Bytes(), trusted)
	if err != nil {
		w.t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	prefix := "/" + meta.Version + "/"
	w.files[prefix+AssetName("linux", "amd64")] = bin
	w.files[prefix+MetadataFile] = metaBytes
	w.files[prefix+ThirdPartyAsset] = pins
	w.files[prefix+ChecksumsFile] = sums.Bytes()
	w.files[prefix+SignatureFile] = sig
}

func (w *releaseWorld) replace(version, asset string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files["/"+version+"/"+asset] = data
}

// fakeService records the manager's calls and runs hooks.
type fakeService struct {
	mu      sync.Mutex
	events  []string
	onStop  func()
	onStart func()
	path    string
}

func (f *fakeService) record(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeService) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeService) Unit(instance string) ([]byte, error) { return []byte("unit " + instance), nil }
func (f *fakeService) UnitPath() string                     { return f.path }
func (f *fakeService) Register(context.Context, []byte) error {
	f.record("register")
	return nil
}

func (f *fakeService) Start(context.Context) error {
	f.record("start")
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

func (f *fakeService) Stop(context.Context) error {
	f.record("stop")
	if f.onStop != nil {
		f.onStop()
	}
	return nil
}

// fakeServer is a server control with a recorded drain.
type fakeServer struct {
	up      bool
	svc     *fakeService
	drained DrainReport
	onDrain func()
}

func (f *fakeServer) Reachable(context.Context) bool { return f.up }
func (f *fakeServer) Drain(context.Context) (DrainReport, error) {
	f.svc.record("drain")
	if f.onDrain != nil {
		f.onDrain()
	}
	return f.drained, nil
}

// instanceWorld is an installed instance at v1.0.0 with a database.
type instanceWorld struct {
	dir    string
	layout Layout
	svc    *fakeService
	server *fakeServer
	pins   *pinWorld
	meta   Metadata // v1.0.0
}

func newInstance(t *testing.T) *instanceWorld {
	t.Helper()
	w := &instanceWorld{dir: filepath.Join(t.TempDir(), "instance"), svc: &fakeService{}, pins: newPinWorld(t, "1.3.1")}
	w.layout = Layout{Dir: w.dir}
	w.server = &fakeServer{up: true, svc: w.svc, drained: DrainReport{Drained: true}}
	w.svc.path = filepath.Join(t.TempDir(), "loop.service")
	ctx := context.Background()
	schema, minReader, err := embeddedSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w.meta = Metadata{Version: "v1.0.0", Schema: schema, MinReaderSchema: minReader, Pins: w.pins.pins}
	self := filepath.Join(t.TempDir(), "loop")
	if err := os.WriteFile(self, []byte("binary v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Layout: w.layout, Service: w.svc, Prov: w.pins.prov, Pins: w.pins.pins, Self: self, Meta: w.meta}
	if _, err := in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, w.layout.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	w.svc.events = nil
	return w
}

func (w *instanceWorld) upgrader(rw *releaseWorld) *Upgrader {
	return &Upgrader{
		Host:   Host{Layout: w.layout, Service: w.svc, Server: w.server},
		Source: rw.source(), Keys: []PublicKey{rw.pub}, Platform: testPlatform, Prov: w.pins.prov, Self: w.meta,
		Fetcher: rw.fetcher(),
	}
}

// tree lists every file under dir with a digest of its content, so a test
// can prove a refused step changed nothing.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == rpc.LockFileName {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			lines = append(lines, rel+" -> "+target)
		case d.IsDir():
			lines = append(lines, rel+"/")
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			lines = append(lines, rel+" "+hex.EncodeToString(sum[:6]))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
