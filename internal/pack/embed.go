package pack

import (
	"io/fs"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	packsrc "github.com/iantanwx/loop/pack"
)

// Source is the pack source the binary embeds.
func Source() fs.FS { return packsrc.Files }

// Brief is the pinned pack this release injects into each run (R36,
// KTD19). Its version is the release's. The scheduler and the room
// service take it as their brief.Pack. An error means the embedded pack
// does not lay out, which the pack test refuses before a release.
func Brief() (brief.Pack, error) {
	tree, err := Layout(Source())
	if err != nil {
		return brief.Pack{}, err
	}
	agents, err := Agents(Source())
	if err != nil {
		return brief.Pack{}, err
	}
	return brief.Pack{Version: cli.Version, FS: tree, Agents: agents}, nil
}
