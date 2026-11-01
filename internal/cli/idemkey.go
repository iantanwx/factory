package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iantanwx/loop/internal/hexid"
)

// keyScheme is the version of the key canonicalisation. It is frozen: a
// verb present in two consecutive schemas must derive the same key in both
// releases (KTD13), so a change here needs a new scheme and a release that
// accepts both.
const keyScheme = 1

// KeyParts are the inputs of one write's idempotency key (KTD13).
type KeyParts struct {
	RunID          string
	Verb           string
	Args           []string
	Flags          map[string]string // canonical; reserved fields are ignored
	ExpectedStatus string
	Seq            int64
	ContentHash    string
	// RequestID is the coordinator's own durable request id, when it gave
	// one. Its session outlives any run, so the id it keeps joins the key.
	RequestID string
}

type canonicalFlag struct {
	Name  string `json:"n"`
	Value string `json:"v"`
}

type canonicalCall struct {
	Verb  string          `json:"verb"`
	Args  []string        `json:"args"`
	Flags []canonicalFlag `json:"flags"`
}

func canonicalCallOf(verb string, args []string, flags map[string]string) canonicalCall {
	c := canonicalCall{Verb: verb, Args: args, Flags: []canonicalFlag{}}
	if c.Args == nil {
		c.Args = []string{}
	}
	for name, value := range flags {
		if strings.HasPrefix(name, ReservedPrefix) {
			continue
		}
		c.Flags = append(c.Flags, canonicalFlag{name, value})
	}
	sort.Slice(c.Flags, func(i, j int) bool { return c.Flags[i].Name < c.Flags[j].Name })
	return c
}

// ArgsHash is the hash of a call's verb, arguments and canonical flags. The
// server stores it with each key, and the CLI matches pending entries on it.
func ArgsHash(verb string, args []string, flags map[string]string) string {
	raw, err := json.Marshal(canonicalCallOf(verb, args, flags))
	if err != nil {
		panic(err) // strings only; Marshal cannot fail
	}
	return hexid.SHA256(raw)
}

// DeriveKey returns the idempotency key of one write. Equal parts give an
// equal key in every release that keeps keyScheme.
func DeriveKey(p KeyParts) string {
	doc := struct {
		Scheme  int    `json:"scheme"`
		Run     string `json:"run"`
		Args    string `json:"args"`
		Status  string `json:"status"`
		Seq     int64  `json:"seq"`
		Content string `json:"content"`
		Request string `json:"request"`
		Verb    string `json:"verb"`
	}{
		Scheme: keyScheme, Run: p.RunID, Args: ArgsHash(p.Verb, p.Args, p.Flags),
		Status: p.ExpectedStatus, Seq: p.Seq, Content: p.ContentHash, Request: p.RequestID, Verb: p.Verb,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("k%d-%s", keyScheme, hex.EncodeToString(sum[:]))
}

// pushesHead reports whether a verb sends the checkout's HEAD for the
// server to push: branch push, and prd submit, which pushes the PRD.
func pushesHead(path string) bool { return path == "branch push" || path == "prd submit" }

// ContentHash is the hash of the content a write carries: the local HEAD
// sha for a verb that pushes it, the --body text, or the bytes of the
// --file file and of each --evidence file. It is "" for a write that
// carries no content.
func ContentHash(ctx context.Context, inv Invocation, checkout, cwd string) (string, error) {
	switch {
	case pushesHead(inv.Verb.Path):
		head, err := CheckoutHead(ctx, checkout)
		if err != nil {
			return "", err
		}
		return HeadContentHash(head), nil
	case inv.Flags["body"] != "":
		return hashText("body", inv.Flags["body"]), nil
	case inv.Flags["file"] != "":
		path := inv.Flags["file"]
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --file: %w", err)
		}
		h := hashText("file", string(data))
		if dir := inv.Flags["evidence"]; dir != "" {
			files, err := ReadEvidence(dir, cwd)
			if err != nil {
				return "", err
			}
			h = hashText("evidence", h+"\x00"+evidenceDigest(files))
		}
		return h, nil
	}
	return "", nil
}

// CheckoutHead reads the full sha of the checkout's HEAD.
func CheckoutHead(ctx context.Context, checkout string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", checkout, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read the checkout's HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadContentHash is the content hash of a `branch push` of head.
func HeadContentHash(head string) string { return hashText("head", head) }

func hashText(kind, text string) string {
	return kind + ":" + hexid.SHA256([]byte(kind+"\x00"+text))
}
