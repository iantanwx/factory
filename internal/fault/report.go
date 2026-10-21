package fault

import (
	"context"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// report is `loop fault report --file <path>`. The CLI read the file and
// sent its text. A report is idempotent like every write (KTD13): the RPC
// server replays a retry with the same key, so a retry gets the same id.
func (s *Service) report(ctx context.Context, call *rpc.Call) cli.Result {
	text, ok := cli.FileContent(call.Inv)
	if !ok {
		return cli.Reject(cli.CodeUsage, "no_file", "--file names the report file; the CLI reads it and sends its text")
	}
	if len(text) > MaxReportBytes {
		return cli.Reject(cli.CodeUsage, "report_too_large",
			fmt.Sprintf("the report holds %d bytes; a report holds %d bytes at most. Keep the command, its envelope and what you expected", len(text), MaxReportBytes))
	}
	if strings.TrimSpace(text) == "" {
		return cli.Reject(cli.CodeUsage, "empty_report", "the report file is empty; name the command, its envelope and what you expected")
	}
	if r := s.Redactor.Refusal("secret_in_report", "report", text); r != nil {
		return r.Result()
	}
	f, err := s.Report(ctx, call.Principal, text)
	if err != nil {
		return cli.Internal(err)
	}
	f.Installed = s.release()
	return cli.Found(f)
}

// Report stores one fault for p and returns it. It checks neither the size
// nor secrets: the verb does.
func (s *Service) Report(ctx context.Context, p rpc.Principal, text string) (Fault, error) {
	f := Fault{Status: Open, Text: text, Reporter: p.Kind, RunID: p.RunID, Project: p.Project, Item: p.Item, Stage: p.Stage,
		Release: s.release()}
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		f.ReportedAt, f.UpdatedAt = now, now
		var n int64
		err := tx.QueryRow(`INSERT INTO faults (text, reporter_kind, run_id, project, item, stage, release, status, reported_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING n`,
			f.Text, f.Reporter, f.RunID, f.Project, f.Item, f.Stage, f.Release, f.Status, now, now).Scan(&n)
		f.ID = FormatID(n)
		return err
	})
	if err != nil {
		return Fault{}, err
	}
	if s.Changed != nil {
		s.Changed()
	}
	return f, nil
}
