package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// File names inside an instance directory and a checkout.
const (
	// InstanceEnv names the instance directory for the CLI. It holds a
	// location, never a credential. The CLI reads the host-admin credential
	// only from AdminCredentialFile inside that directory.
	InstanceEnv = "LOOP_INSTANCE"
	// AdminCredentialFile holds the host-admin credential, mode 0600
	// (KTD12). `loop install` writes it.
	AdminCredentialFile = "admin.credential"
	// SocketName is the file name of the server's socket.
	SocketName = "loop.sock"
	// LoopDir is the directory in a checkout that holds the run's token
	// and its pending writes.
	LoopDir = ".loop"
	// TokenFileName is the run token file inside LoopDir.
	TokenFileName = "token"
	// PendingFileName is the pending-write log inside LoopDir (KTD13).
	PendingFileName = "pending.jsonl"
)

// The files the server writes into a checkout, relative to its root
// (KTD19). Every other path under LoopDir, such as .loop/config.yaml,
// .loop/secrets.yaml or .loop/policy/<role>.md, belongs to the project's
// repository.
const (
	BriefJSONFile   = LoopDir + "/brief.json"
	BriefMDFile     = LoopDir + "/brief.md"
	PromptFile      = LoopDir + "/prompt.md"
	EnvFile         = ".env"
	TokenPath       = LoopDir + "/" + TokenFileName
	PendingFile     = LoopDir + "/" + PendingFileName
	PendingLockFile = LoopDir + "/pending.lock"
	// EnvRecordFile names each .env the server wrote into the checkout.
	EnvRecordFile = LoopDir + "/env-files"
)

// InjectedFiles lists every file the server writes into a checkout. The
// run start excludes them and refuses a repository that tracks one, and
// the push scan refuses a commit that touches one.
func InjectedFiles() []string {
	return []string{BriefJSONFile, BriefMDFile, PromptFile, EnvFile, TokenPath, PendingFile, PendingLockFile, EnvRecordFile}
}

// maxSocketPath keeps a socket path below the sun_path limit: 108 bytes
// on Linux and 104 on macOS, each with the terminating NUL.
const maxSocketPath = 100

// DefaultInstanceDir is ~/.local/state/loop.
func DefaultInstanceDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "loop"), nil
}

// InstanceDir is the instance the CLI talks to: LOOP_INSTANCE, or the
// default.
func InstanceDir() (string, error) {
	if dir := os.Getenv(InstanceEnv); dir != "" {
		return filepath.Abs(dir)
	}
	return DefaultInstanceDir()
}

// SocketPath is the socket of the instance. It sits in a private run
// directory inside the instance. When that path is too long for a Unix
// socket, it sits in a private directory under the system temp directory,
// named by the user and a digest of the instance path.
func SocketPath(instance string) string {
	p := filepath.Join(instance, "run", SocketName)
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(instance))
	return filepath.Join(os.TempDir(), fmt.Sprintf("loop-%d-%s", os.Getuid(), hex.EncodeToString(sum[:10])), SocketName)
}

// AdminCredentialPath is the host-admin credential file of the instance.
func AdminCredentialPath(instance string) string {
	return filepath.Join(instance, AdminCredentialFile)
}

// ReadAdminCredential reads the host-admin credential file. It refuses a
// symlink, a file that another user owns, and a file that any other user
// can read. It never reads the environment.
func ReadAdminCredential(instance string) (string, error) {
	path := AdminCredentialPath(instance)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %v; want 0600", path, info.Mode().Perm())
	}
	if uid, ok := fileOwner(info); ok && uid != os.Getuid() {
		return "", fmt.Errorf("%s belongs to another user", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	cred := strings.TrimSpace(string(data))
	if cred == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return cred, nil
}

// TokenFile is the content of .loop/token in a checkout. The server writes
// it when it starts a run, a room or the coordinator. Token is the secret.
// The other fields let the CLI derive idempotency keys without a call.
type TokenFile struct {
	Token   string `json:"token"`
	RunID   string `json:"run_id"`
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Item    string `json:"item,omitempty"`
}

// ErrNoCheckout means no directory from the working directory up holds a
// .loop/token file.
var ErrNoCheckout = errors.New("no .loop/token in this directory or above it")

// FindCheckout walks up from dir to the first directory that holds
// .loop/token. That directory is the run's checkout.
func FindCheckout(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		info, err := os.Stat(filepath.Join(dir, LoopDir, TokenFileName))
		if err == nil && info.Mode().IsRegular() {
			return dir, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoCheckout
		}
		dir = parent
	}
}

// ReadTokenFile reads checkout/.loop/token.
func ReadTokenFile(checkout string) (TokenFile, error) {
	data, err := os.ReadFile(filepath.Join(checkout, LoopDir, TokenFileName))
	if err != nil {
		return TokenFile{}, err
	}
	var tf TokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return TokenFile{}, fmt.Errorf("read %s: the token file is not JSON", filepath.Join(checkout, LoopDir, TokenFileName))
	}
	if tf.Token == "" || tf.RunID == "" {
		return TokenFile{}, fmt.Errorf("read %s: the token file lacks a token or a run id", filepath.Join(checkout, LoopDir, TokenFileName))
	}
	return tf, nil
}

// WriteTokenFile writes checkout/.loop/token at mode 0600, through a
// temporary file and a rename.
func WriteTokenFile(checkout string, tf TokenFile) error {
	dir := filepath.Join(checkout, LoopDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(tf)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, TokenFileName))
}

// fileOwner returns the owner uid of a file on a Unix system.
func fileOwner(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
