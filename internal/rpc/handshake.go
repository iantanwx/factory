package rpc

import (
	"fmt"

	"github.com/iantanwx/loop/internal/cli"
)

// checkVersion is the version handshake (R23, KTD13). A CLI of another
// version is refused, except that a run started under the previous release
// may call any verb present in this server's schema. The caller reaches
// here only for a verb this server's registry holds, and the CLI holds it
// too, so the verb is present in both schemas.
func (s *Server) checkVersion(clientVersion string, p Principal) *cli.Result {
	if clientVersion == s.opts.Version {
		return nil
	}
	if p.RunID != "" && s.opts.PreviousVersion != "" &&
		clientVersion == s.opts.PreviousVersion && p.Release == clientVersion {
		return nil
	}
	res := cli.Refuse(cli.Refusal{
		Code: cli.CodeVersionMismatch, Rule: "version_mismatch",
		Hint: fmt.Sprintf("this CLI is %q and the server is %q; use the loop binary of the server's release", clientVersion, s.opts.Version),
	})
	return &res
}
