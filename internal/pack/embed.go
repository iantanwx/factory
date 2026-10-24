package pack

import (
	"fmt"
	"io/fs"
	"maps"
	"strings"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	packsrc "github.com/iantanwx/loop/pack"
)

// Source is the pack source the binary embeds.
func Source() fs.FS { return packsrc.Files }

// Brief is the pinned pack this release injects into each run (R36,
// KTD19). Its version is the release's. The scheduler and the room
// service take it as their brief.Pack. fetched holds one directory for
// each third-party pin, named by ThirdParty.Name; nil holds none. Each pin
// whose directory is missing or fails its hash is left out of the pack and
// returned (ADR 0007). An error means the embedded pack does not lay out,
// which the pack test refuses before a release.
func Brief(fetched fs.FS) (brief.Pack, []ThirdParty, error) {
	return BriefOf(Source(), fetched)
}

// BriefOf is Brief over the pack source src and the pins src holds.
func BriefOf(src, fetched fs.FS) (brief.Pack, []ThirdParty, error) {
	tree, err := layout(src)
	if err != nil {
		return brief.Pack{}, nil, err
	}
	agents, err := Agents(src)
	if err != nil {
		return brief.Pack{}, nil, err
	}
	pins, err := ReadThirdParty(src)
	if err != nil {
		return brief.Pack{}, nil, err
	}
	var left []ThirdParty
	for _, p := range pins {
		skill, ok := verified(fetched, p)
		if !ok {
			left = append(left, p)
			continue
		}
		maps.Copy(tree, skill)
	}
	return brief.Pack{Version: cli.Version, FS: tree, Agents: agents}, left, nil
}

// verified lays the pin's fetched directory out as loop-<name>/, when its
// tree hash matches the pin and its SKILL.md names it. Each file keeps its
// bytes and its executable bit. The one front matter line name: <name>
// becomes name: loop-<name>. The files are read once, and the hash is
// taken over what was read.
func verified(fetched fs.FS, p ThirdParty) (fstest.MapFS, bool) {
	if fetched == nil {
		return nil, false
	}
	sub, err := fs.Sub(fetched, p.Name)
	if err != nil {
		return nil, false
	}
	read := fstest.MapFS{}
	err = fs.WalkDir(sub, ".", func(name string, d fs.DirEntry, err error) error {
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
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			return err
		}
		read[name] = &fstest.MapFile{Data: data, Mode: info.Mode().Perm()}
		return nil
	})
	if err != nil || len(read) == 0 {
		return nil, false
	}
	if sum, err := TreeHash(read); err != nil || sum != p.SHA256 {
		return nil, false
	}
	skill, ok := read["SKILL.md"]
	if !ok {
		return nil, false
	}
	renamed, ok := rename(skill.Data, p.Name)
	if !ok {
		return nil, false
	}
	dir := brief.SkillPrefix + p.Name
	out := fstest.MapFS{}
	for name, f := range read {
		out[dir+"/"+name] = f
	}
	out[dir+"/SKILL.md"] = &fstest.MapFile{Data: renamed, Mode: skill.Mode}
	return out, true
}

// rename changes the line name: <name> of the front matter that opens a
// SKILL.md to name: loop-<name>, and keeps every other byte.
func rename(skill []byte, name string) ([]byte, bool) {
	lines := strings.SplitAfter(string(skill), "\n")
	for i, line := range lines {
		text := strings.TrimRight(line, "\r\n")
		switch {
		case i == 0 && text != "---":
			return nil, false
		case i > 0 && text == "---":
			return nil, false
		case i > 0 && text == "name: "+name:
			lines[i] = "name: " + brief.SkillPrefix + name + line[len(text):]
			return []byte(strings.Join(lines, "")), true
		}
	}
	return nil, false
}
