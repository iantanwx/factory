// Package fault keeps loop fault reports (R55, KTD31). A run, a room
// or the coordinator reports a fault in the loop itself with
// `loop fault report --file <path>`. The server stores the report in
// SQLite for the whole instance and answers with an id, F-<n>, that never
// changes. A fault is never a work item and never enters beads. The
// coordinator acknowledges each fault and resolves it as fixed in a
// release, as a duplicate of another fault, or as not a fault with a
// reason. `loop fault show` returns the status, the resolution and the
// release this host runs, so a run can see that a fix exists and is not
// yet installed.
package fault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Fault statuses. A fault moves open, then acknowledged, then one of the
// three resolutions. A resolve may also start from open.
const (
	Open         = "open"
	Acknowledged = "acknowledged"
	Fixed        = "fixed"
	Duplicate    = "duplicate"
	NotAFault    = "not-a-fault"
)

// MaxReportBytes bounds one report's text.
const MaxReportBytes = 64 << 10

// ErrNotFound is the answer for an id the server never gave out.
var ErrNotFound = errors.New("fault: no such fault")

var idPattern = regexp.MustCompile(`^F-([1-9][0-9]*)$`)

// FormatID is the id of row n.
func FormatID(n int64) string { return "F-" + strconv.FormatInt(n, 10) }

// ParseID returns the row of id, or false when id is not F-<n>.
func ParseID(id string) (int64, bool) {
	m := idPattern.FindStringSubmatch(strings.TrimSpace(id))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return n, err == nil
}

// Fault is one fault as `loop fault show` prints it. Times are unix
// milliseconds. Installed is the release this server runs.
type Fault struct {
	ID             string `json:"fault"`
	Status         string `json:"status"`
	Text           string `json:"text,omitempty"`
	Reporter       string `json:"reporter"`
	RunID          string `json:"run_id"`
	Project        string `json:"project"`
	Item           string `json:"item"`
	Stage          string `json:"stage"`
	Release        string `json:"release"`
	FixedIn        string `json:"fixed_in,omitempty"`
	DuplicateOf    string `json:"duplicate_of,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ResolvedBy     string `json:"resolved_by,omitempty"`
	ReportedAt     int64  `json:"reported_at"`
	AcknowledgedAt int64  `json:"acknowledged_at,omitempty"`
	ResolvedAt     int64  `json:"resolved_at,omitempty"`
	UpdatedAt      int64  `json:"updated_at"`
	Installed      string `json:"installed,omitempty"`
}

// Service serves the fault verbs.
type Service struct {
	Store    *store.Store
	Redactor *secrets.Redactor
	// Release is the release this server runs. cli.Version when empty.
	Release string
	// Changed is called after a new fault is stored. Optional. The inbox's
	// notifier rings the coordinator from it.
	Changed func()
}

// Register sets the handlers of the fault verbs.
func (s *Service) Register(srv *rpc.Server) error {
	for path, h := range map[string]rpc.Handler{
		"fault report":  s.report,
		"fault show":    s.show,
		"fault list":    s.list,
		"fault ack":     s.ack,
		"fault resolve": s.resolve,
	} {
		if err := srv.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) release() string {
	if s.Release != "" {
		return s.Release
	}
	return cli.Version
}

const columns = `n, status, text, reporter_kind, run_id, project, item, stage, release, fixed_in, duplicate_of, reason,
	resolved_by, reported_at, coalesce(acknowledged_at, 0), coalesce(resolved_at, 0), updated_at`

func scan(sc store.Scanner) (Fault, error) {
	var f Fault
	var n int64
	err := sc.Scan(&n, &f.Status, &f.Text, &f.Reporter, &f.RunID, &f.Project, &f.Item, &f.Stage, &f.Release, &f.FixedIn,
		&f.DuplicateOf, &f.Reason, &f.ResolvedBy, &f.ReportedAt, &f.AcknowledgedAt, &f.ResolvedAt, &f.UpdatedAt)
	f.ID = FormatID(n)
	return f, err
}

func query(ctx context.Context, q store.Querier, where string, args ...any) ([]Fault, error) {
	rs, err := q.QueryContext(ctx, `SELECT `+columns+` FROM faults WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Fault
	for rs.Next() {
		f, err := scan(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rs.Err()
}

// Get reads one fault. It returns ErrNotFound for an id the server never
// gave out.
func (s *Service) Get(ctx context.Context, id string) (Fault, error) {
	n, ok := ParseID(id)
	if !ok {
		return Fault{}, ErrNotFound
	}
	var out []Fault
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = query(ctx, tx, `n = ?`, n)
		return err
	})
	if err != nil {
		return Fault{}, err
	}
	if len(out) == 0 {
		return Fault{}, ErrNotFound
	}
	return out[0], nil
}

// Exists reports whether the server gave out id. `run finish --fault`
// checks it inside its own write.
func Exists(tx *store.Tx, id string) (bool, error) {
	n, ok := ParseID(id)
	if !ok {
		return false, nil
	}
	var one int
	err := tx.QueryRow(`SELECT 1 FROM faults WHERE n = ?`, n).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Pending lists the open and acknowledged faults, oldest first. The inbox
// shows each one as an entry that waits on the coordinator.
func Pending(ctx context.Context, tx *sql.Tx) ([]Fault, error) {
	return query(ctx, tx, `status IN (?, ?) ORDER BY n`, Open, Acknowledged)
}

// ForBrief lists the faults filed from project and stage that a run's
// brief names: open, acknowledged, and fixed within brief.FaultWindow
// before now.
func ForBrief(ctx context.Context, st *store.Store, project, stage string, now time.Time) ([]brief.Fault, error) {
	var fs []Fault
	err := st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		fs, err = query(ctx, tx, `project = ? AND stage = ? AND (status IN (?, ?) OR status = ? AND resolved_at >= ?) ORDER BY n`,
			project, stage, Open, Acknowledged, Fixed, now.Add(-brief.FaultWindow).UnixMilli())
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]brief.Fault, 0, len(fs))
	for _, f := range fs {
		out = append(out, brief.Fault{ID: f.ID, Status: f.Status, FixedIn: f.FixedIn})
	}
	return out, nil
}

// show is `loop fault show <id>`.
func (s *Service) show(ctx context.Context, call *rpc.Call) cli.Result {
	id := call.Inv.Arg("fault")
	if _, ok := ParseID(id); !ok {
		return cli.Reject(cli.CodeUsage, "bad_fault", fmt.Sprintf("%q is no fault id; a fault id is F-<n>", id))
	}
	f, err := s.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return cli.Reject(cli.CodeNotFound, "no_fault", fmt.Sprintf("the server gave out no fault %s", id))
	}
	if err != nil {
		return cli.Internal(err)
	}
	f.Installed = s.release()
	return cli.Found(f)
}

// list is `loop fault list`: newest first, without each report's text. A
// coordinator lists only the faults it reaches. A fault with no project is
// the instance's, and only the loop project's coordinator reaches it.
func (s *Service) list(ctx context.Context, call *rpc.Call) cli.Result {
	where, args := []string{"1 = 1"}, []any{}
	for _, col := range []string{"project", "stage", "status"} {
		if v, ok := call.Inv.Flags[col]; ok {
			where, args = append(where, col+" = ?"), append(args, v)
		}
	}
	var fs []Fault
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		fs, err = query(ctx, tx, strings.Join(where, " AND ")+` ORDER BY n DESC`, args...)
		return err
	})
	if err != nil {
		return cli.Internal(err)
	}
	c, scoped := rpc.CallerFrom(ctx)
	out := make([]Fault, 0, len(fs))
	for _, f := range fs {
		if scoped && c.Reach != coord.ReachUnscoped {
			keep := c.Reach == coord.ReachLoop
			if f.Project != "" {
				if keep, err = c.Reaches(ctx, coord.Record{Kind: coord.KindFault, ID: f.ID, Project: f.Project}); err != nil {
					return cli.Internal(err)
				}
			}
			if !keep {
				continue
			}
		}
		f.Text = ""
		f.Installed = s.release()
		out = append(out, f)
	}
	return cli.List(out)
}
