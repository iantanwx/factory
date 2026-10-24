package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/brief"
)

// ThirdPartyFile is the pin file's path in the pack source.
const ThirdPartyFile = "third-party.json"

// ThirdParty is one pinned third-party skill (ADR 0007, decision 7). The
// release does not hold its files. A host verb fetches them at Commit,
// and TreeHash checks them against SHA256. A run receives the skill as
// loop-<Name>.
type ThirdParty struct {
	// Name is the upstream skill name, such as impeccable.
	Name string `json:"name"`
	// Repo is owner/name on GitHub.
	Repo string `json:"repo"`
	// Commit is the pinned commit.
	Commit string `json:"commit"`
	// Path is the skill's directory in the repository. "" is the root.
	Path string `json:"path"`
	// SHA256 is the TreeHash of that directory at Commit.
	SHA256 string `json:"sha256"`
}

// ReadThirdParty reads and checks the pin file of a pack source. The file is
// {"skills": [ThirdParty, ...]}, and an unknown field is refused. A source
// with no pin file holds no pins. Each pin it returns holds every invariant
// of ThirdParty except the one Layout checks, so no other code checks a pin
// again.
func ReadThirdParty(src fs.FS) ([]ThirdParty, error) {
	raw, err := fs.ReadFile(src, ThirdPartyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pack: read %s: %w", ThirdPartyFile, err)
	}
	var file struct {
		Skills []json.RawMessage `json:"skills"`
	}
	if err := strict(raw, &file); err != nil {
		return nil, fmt.Errorf("pack: %s: %w", ThirdPartyFile, err)
	}
	out := make([]ThirdParty, 0, len(file.Skills))
	for i, entry := range file.Skills {
		// A strict decode fills the known fields before it reports an
		// unknown one, so the error can name the pin.
		var p ThirdParty
		err := strict(entry, &p)
		if err == nil {
			err = p.check()
		}
		if err == nil && slices.ContainsFunc(out, func(q ThirdParty) bool { return q.Name == p.Name }) {
			err = errors.New("another pin has this name")
		}
		if err != nil {
			label := strconv.Quote(p.Name)
			if p.Name == "" {
				label = strconv.Itoa(i)
			}
			return nil, fmt.Errorf("pack: %s: pin %s: %w", ThirdPartyFile, label, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (p ThirdParty) check() error {
	switch {
	case !validName(p.Name):
		return fmt.Errorf("name %q is not lower case letters, digits and dashes", p.Name)
	case strings.HasPrefix(p.Name, brief.SkillPrefix):
		return fmt.Errorf("name %q is in the %s* namespace, which the pack adds", p.Name, brief.SkillPrefix)
	case !lowerHex(p.Commit, 40):
		return fmt.Errorf("commit %q is not 40 lower-case hex characters", p.Commit)
	case !lowerHex(p.SHA256, 64):
		return fmt.Errorf("sha256 %q is not 64 lower-case hex characters", p.SHA256)
	case p.Path != "" && (p.Path == "." || !fs.ValidPath(p.Path)):
		return fmt.Errorf("path %q is not a clean relative path inside the repository", p.Path)
	}
	return nil
}

func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ThirdPartyPins are the pins this binary embeds. The pack test reads the
// same file through ReadThirdParty, so a bad pin never reaches a release.
func ThirdPartyPins() []ThirdParty {
	pins, err := ReadThirdParty(Source())
	if err != nil {
		panic(err)
	}
	return pins
}

// TreeHash is the hex sha256 of tree: for each regular file, in byte order of its
// slash path, "<path>\x00<mode>\x00<len>\x00" then its bytes, where mode is "755"
// when any executable bit is set and "644" otherwise. Directories, other mode
// bits and timestamps do not count. A member that is neither a regular file
// nor a directory is refused.
func TreeHash(tree fs.FS) (string, error) {
	exec := map[string]bool{}
	err := fs.WalkDir(tree, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		exec[name] = info.Mode()&0o111 != 0
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("pack: tree hash: %w", err)
	}
	h := sha256.New()
	for _, name := range slices.Sorted(maps.Keys(exec)) {
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return "", fmt.Errorf("pack: tree hash: %w", err)
		}
		mode := "644"
		if exec[name] {
			mode = "755"
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%d\x00", name, mode, len(data)) // a hash never fails a write
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
