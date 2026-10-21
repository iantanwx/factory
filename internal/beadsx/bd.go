package beadsx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SchemaVersion is the bd JSON schema version the adapter parses.
const SchemaVersion = 1

// DefaultTimeout bounds one bd call when the caller's context has no deadline.
const DefaultTimeout = 30 * time.Second

// BDConfig configures the real client.
type BDConfig struct {
	Binary    string        // path to the pinned bd
	Workspace string        // the directory that holds .beads
	Home      string        // the isolated HOME for bd (Layout.Home)
	Actor     string        // the audit actor; empty means "loop"
	Timeout   time.Duration // per call; zero means DefaultTimeout
}

// BD is the Client around the pinned `bd --json`.
type BD struct {
	cfg BDConfig
	env []string
}

var _ Client = (*BD)(nil)

// NewBD checks the bd version and returns the client. It refuses any bd
// that is not exactly PinnedBD. It does not need the workspace to exist.
func NewBD(ctx context.Context, cfg BDConfig) (*BD, error) {
	if cfg.Binary == "" || cfg.Workspace == "" || cfg.Home == "" {
		return nil, fmt.Errorf("%w: bd binary, workspace and home are required", ErrInvalid)
	}
	if cfg.Actor == "" {
		cfg.Actor = "loop"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	b := &BD{cfg: cfg, env: bdEnv(cfg.Home, filepath.Join(cfg.Workspace, ".beads"), cfg.Actor)}
	v, err := b.Version(ctx)
	if err != nil {
		return nil, err
	}
	if v != PinnedBD {
		return nil, fmt.Errorf("%w: bd %s at %s, want %s", ErrVersion, v, cfg.Binary, PinnedBD)
	}
	return b, nil
}

// bdEnv is the whole environment of every bd call. It inherits nothing but
// PATH, so a BEADS_* variable in the operator's shell cannot redirect bd.
//
// bd 1.3.1 turns anonymous usage metrics on by default, and it writes that
// choice into $HOME/.config/bd/config.yaml. HOME and XDG_CONFIG_HOME point at
// the instance's own directory, which EnsureHome seeds with metrics off.
// DO_NOT_TRACK=1 turns metrics off a second time, without any file.
func bdEnv(home, beadsDir, actor string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"DO_NOT_TRACK=1",
		"BEADS_DIR=" + beadsDir,
		"BEADS_ACTOR=" + actor,
		"BEADS_DOLT_AUTO_START=0",
		"BD_NON_INTERACTIVE=1",
		"BD_JSON_ENVELOPE=1",
		"BD_NO_PAGER=1",
		"BD_NO_DEP_TYPE_WARNING=1",
		"NO_COLOR=1",
		"TZ=UTC",
	}
}

// run executes one bd call and returns its stdout. Every failure is a
// *BDError. A cancelled or expired context kills bd and its process group.
func (b *BD) run(ctx context.Context, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.cfg.Timeout)
		defer cancel()
	}
	return runBD(ctx, b.cfg.Binary, b.cfg.Workspace, b.env, args...)
}

func runBD(ctx context.Context, bin, dir string, env []string, args ...string) ([]byte, error) {
	op := ""
	if len(args) > 0 {
		op = args[0]
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole group, so no child of bd outlives the call.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, &BDError{Op: op, Exit: -1, Message: "call interrupted; result unknown",
			Kind: ErrUnavailable, Cause: ctxErr}
	}
	if err == nil {
		return stdout.Bytes(), nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return nil, &BDError{Op: op, Exit: -1, Message: err.Error(), Kind: ErrFailed, Cause: err}
	}
	exit := ee.ExitCode()
	msg := errorMessage(stdout.Bytes(), stderr.Bytes())
	kind := classify(exit, msg+"\n"+stderr.String()+"\n"+stdout.String())
	if exit == -1 {
		// Killed by a signal that was not ours: the result is unknown.
		kind = ErrUnavailable
	}
	return nil, &BDError{Op: op, Exit: exit, Message: msg, Kind: kind}
}

// errorMessage finds bd's error text: the envelope's data.error on stdout, or
// on the last JSON line of stderr, or else the stderr text.
func errorMessage(stdout, stderr []byte) string {
	try := func(b []byte) string {
		var env struct {
			Data struct {
				Error  string          `json:"error"`
				Code   string          `json:"code"`
				Failed json.RawMessage `json:"failed"`
			} `json:"data"`
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(bytes.TrimSpace(b), &env) != nil {
			return ""
		}
		parts := []string{env.Data.Error, env.Data.Code, env.Error, env.Code}
		if len(env.Data.Failed) > 0 {
			parts = append(parts, string(env.Data.Failed))
		}
		return strings.TrimSpace(strings.Join(nonEmpty(parts), " "))
	}
	if m := try(stdout); m != "" {
		return m
	}
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
			if m := try([]byte(lines[i])); m != "" {
				return m
			}
		}
	}
	return strings.TrimSpace(string(stderr))
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// decode reads the JSON envelope bd writes under BD_JSON_ENVELOPE=1 and
// checks its schema_version.
func decode(op string, out []byte, v any) error {
	var env struct {
		Data          json.RawMessage `json:"data"`
		SchemaVersion *int            `json:"schema_version"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return &BDError{Op: op, Exit: 0, Message: "not JSON: " + truncate(out), Kind: ErrMalformed, Cause: err}
	}
	if env.SchemaVersion == nil || *env.SchemaVersion != SchemaVersion {
		return &BDError{Op: op, Exit: 0, Message: "schema_version is not " + strconv.Itoa(SchemaVersion),
			Kind: ErrMalformed}
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return &BDError{Op: op, Exit: 0, Message: "unexpected data: " + truncate(env.Data), Kind: ErrMalformed, Cause: err}
	}
	return nil
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

// wireIssue is one issue as bd 1.3.1 writes it.
type wireIssue struct {
	ID          string                     `json:"id"`
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	Status      string                     `json:"status"`
	Priority    int                        `json:"priority"`
	IssueType   string                     `json:"issue_type"`
	Assignee    string                     `json:"assignee"`
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
	Metadata    map[string]json.RawMessage `json:"metadata"`
	// Read only by the guard.
	DeferUntil     *time.Time `json:"defer_until"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at"`
}

func (w wireIssue) issue() Issue {
	return Issue{
		ID: w.ID, Title: w.Title, Description: w.Description, Status: w.Status,
		Type: w.IssueType, Priority: w.Priority, Assignee: w.Assignee,
		Metadata: metadataStrings(w.Metadata), CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

// metadataStrings turns stored JSON values into strings. A null is an unset
// key and is dropped. A string keeps its exact text. Any other value, which
// only a writer outside the adapter can store, keeps its JSON text.
func metadataStrings(m map[string]json.RawMessage) map[string]string {
	out := make(map[string]string, len(m))
	for k, raw := range m {
		t := bytes.TrimSpace(raw)
		if len(t) == 0 || bytes.Equal(t, []byte("null")) {
			continue
		}
		var s string
		if t[0] == '"' && json.Unmarshal(t, &s) == nil {
			out[k] = s
			continue
		}
		out[k] = string(t)
	}
	return out
}

// Version implements Client. It runs outside the workspace, so it works
// before the workspace exists.
func (b *BD) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()
	out, err := runBD(ctx, b.cfg.Binary, os.TempDir(), b.env, "version", "--json")
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := decode("version", out, &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", &BDError{Op: "version", Message: "no version field", Kind: ErrMalformed}
	}
	return v.Version, nil
}

// ConfigureStatuses implements Client.
func (b *BD) ConfigureStatuses(ctx context.Context, defs []StatusDef) error {
	if err := ValidateStatuses(defs); err != nil {
		return err
	}
	val := statusConfigValue(defs)
	if val == "" {
		_, err := b.run(ctx, "config", "unset", "status.custom", "--json")
		return err
	}
	_, err := b.run(ctx, "config", "set", "status.custom", val, "--json")
	return err
}

// ConfigureTypes implements Client.
func (b *BD) ConfigureTypes(ctx context.Context, types []string) error {
	if err := ValidateTypes(types); err != nil {
		return err
	}
	val := strings.Join(types, ",")
	if val == "" {
		_, err := b.run(ctx, "config", "unset", "types.custom", "--json")
		return err
	}
	_, err := b.run(ctx, "config", "set", "types.custom", val, "--json")
	return err
}

// Create implements Client.
func (b *BD) Create(ctx context.Context, spec CreateSpec) (Issue, error) {
	if err := spec.Validate(); err != nil {
		return Issue{}, err
	}
	args := []string{"create", "--title", spec.Title, "--priority", strconv.Itoa(spec.Priority), "--json"}
	if spec.Type != "" {
		args = append(args, "--type", spec.Type)
	}
	if spec.Description != "" {
		args = append(args, "--description", spec.Description)
	}
	if len(spec.Metadata) > 0 {
		js, err := metadataJSON(spec.Metadata, nil)
		if err != nil {
			return Issue{}, err
		}
		args = append(args, "--metadata", js)
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return Issue{}, err
	}
	var w wireIssue
	if err := decode("create", out, &w); err != nil {
		return Issue{}, err
	}
	if w.ID == "" {
		return Issue{}, &BDError{Op: "create", Message: "no id in output", Kind: ErrMalformed}
	}
	// create's output omits stored metadata, so read the issue back.
	return b.Get(ctx, w.ID)
}

// Get implements Client.
func (b *BD) Get(ctx context.Context, id string) (Issue, error) {
	if err := validateID(id); err != nil {
		return Issue{}, err
	}
	out, err := b.run(ctx, "show", id, "--json")
	if err != nil {
		return Issue{}, err
	}
	var ws []wireIssue
	if err := decode("show", out, &ws); err != nil {
		return Issue{}, err
	}
	if len(ws) != 1 || ws[0].ID != id {
		return Issue{}, &BDError{Op: "show", Message: fmt.Sprintf("want issue %s, got %d issues", id, len(ws)), Kind: ErrMalformed}
	}
	return ws[0].issue(), nil
}

// List implements Client.
func (b *BD) List(ctx context.Context, f Filter) ([]Issue, error) {
	args := []string{"list", "--json", "--limit", "0"}
	if len(f.Statuses) > 0 {
		args = append(args, "--status", strings.Join(f.Statuses, ","))
	} else {
		args = append(args, "--all")
	}
	if f.Type != "" {
		args = append(args, "--type", f.Type)
	}
	keys := slices.Sorted(maps.Keys(f.Metadata))
	for _, k := range keys {
		args = append(args, "--metadata-field", k+"="+f.Metadata[k])
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var ws []wireIssue
	if err := decode("list", out, &ws); err != nil {
		return nil, err
	}
	res := make([]Issue, 0, len(ws))
	for _, w := range ws {
		is := w.issue()
		// bd matches a metadata field on its stored JSON. Check the string
		// form again, so a number stored by another writer cannot match.
		if f.Match(is) {
			res = append(res, is)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res, nil
}

// Update implements Client. It is one `bd update` call: bd checks every
// guard and writes the status, the assignee and the metadata in one
// transaction, or it writes nothing and exits 13.
func (b *BD) Update(ctx context.Context, id string, spec UpdateSpec) (Issue, error) {
	if err := validateID(id); err != nil {
		return Issue{}, err
	}
	if err := spec.Validate(); err != nil {
		return Issue{}, err
	}
	args := []string{"update", id, "--if-status", spec.IfStatus, "--json"}
	if spec.IfAssignee != nil {
		args = append(args, "--if-assignee", *spec.IfAssignee)
	}
	if spec.Status != "" {
		args = append(args, "--status", spec.Status)
	}
	if spec.Assignee != nil {
		args = append(args, "--assignee", *spec.Assignee)
	}
	if len(spec.SetMetadata)+len(spec.UnsetMetadata) > 0 {
		js, err := metadataJSON(spec.SetMetadata, spec.UnsetMetadata)
		if err != nil {
			return Issue{}, err
		}
		args = append(args, "--metadata", js)
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return Issue{}, err
	}
	var ws []wireIssue
	if err := decode("update", out, &ws); err != nil {
		return Issue{}, err
	}
	if len(ws) != 1 || ws[0].ID != id {
		return Issue{}, &BDError{Op: "update", Message: fmt.Sprintf("want issue %s, got %d issues", id, len(ws)), Kind: ErrMalformed}
	}
	return ws[0].issue(), nil
}

// wireEvent is one journal record of `bd events tail --json`.
type wireEvent struct {
	Seq     int64      `json:"seq"`
	TS      time.Time  `json:"ts"`
	Op      string     `json:"op"`
	IssueID string     `json:"issue_id"`
	Actor   string     `json:"actor"`
	Issue   *wireIssue `json:"issue"`
}

// Events implements Client. bd has no journal filter by issue, so this reads
// the whole retained journal and keeps one issue's records.
func (b *BD) Events(ctx context.Context, id string) ([]Event, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	all, err := b.Tail(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, e := range all {
		if e.IssueID == id {
			out = append(out, e)
		}
	}
	return out, nil
}

// Tail implements Client.
func (b *BD) Tail(ctx context.Context, since int64, limit int) ([]Event, error) {
	args := []string{"events", "tail", "--json", "--since", strconv.FormatInt(since, 10)}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseJournal(out)
}

// Head implements Client. bd 1.3.1 has no verb that prints the head, so
// Head reads the journal's counter row with one read-only `bd sql` query.
// The table and column are bd's own mechanism, and a later bd may rename
// them: TestHeadSurvivesAPruneReal proves the outcome on the pinned bd. The
// counter holds the last seq assigned, in spite of its column name, and a
// prune does not lower it. No row means the journal never recorded.
func (b *BD) Head(ctx context.Context) (int64, error) {
	out, err := b.run(ctx, "sql", "--json", "SELECT next_seq AS head FROM bd_events_seq WHERE id = 0")
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Head *int64 `json:"head"`
	}
	if err := decode("sql", out, &rows); err != nil {
		return 0, err
	}
	switch {
	case len(rows) == 0:
		return 0, nil
	case len(rows) > 1 || rows[0].Head == nil || *rows[0].Head < 0:
		return 0, &BDError{Op: "sql", Message: "journal head: unexpected rows: " + truncate(out), Kind: ErrMalformed}
	}
	return *rows[0].Head, nil
}

// AddDependency implements Client.
func (b *BD) AddDependency(ctx context.Context, from, to string, kind DepKind) error {
	if err := validateID(from); err != nil {
		return err
	}
	if err := validateID(to); err != nil {
		return err
	}
	if kind == "" {
		kind = DepBlocks
	}
	// bd 1.3.1 stores an edge to an id with another prefix as a cross-repo
	// edge and reports success. The server links issues of one project only,
	// so the target must exist here.
	if _, err := b.Get(ctx, to); err != nil {
		return err
	}
	// bd 1.3.1 keeps one edge when the same pair is added again, and it
	// reports success (TestContractReal, DependencyTwice).
	_, err := b.run(ctx, "dep", "add", from, to, "--type", bdDepType(kind), "--json")
	return err
}

// bdBlocks is the bd type that stores a blocks edge. Beads enforces no
// edges, and the server alone decides when a ticket waits (admission and
// dependency-advance). bd 1.3.1 enforces its own blocks type: it refuses
// to move an issue to a done status while a blocker holds any status but
// the built-in closed, and the server's done is a custom status. So a
// blocks edge goes to bd as tracks, a type bd keeps and never enforces
// (TestContractReal, DoneBlockerBlocksNoDone).
const bdBlocks = "tracks"

func bdDepType(k DepKind) string {
	if k == DepBlocks {
		return bdBlocks
	}
	return string(k)
}

func depKind(bdType string) DepKind {
	if bdType == bdBlocks {
		return DepBlocks
	}
	return DepKind(bdType)
}

// Dependencies implements Client. bd 1.3.1 answers an unknown id with exit
// 1 and "no issue found", which classify reads as ErrNotFound.
func (b *BD) Dependencies(ctx context.Context, id string) ([]Dependency, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	out, err := b.run(ctx, "dep", "list", id, "--json")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Kind   string `json:"dependency_type"`
	}
	if err := decode("dep", out, &rows); err != nil {
		return nil, err
	}
	deps := make([]Dependency, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" || r.Status == "" || r.Kind == "" {
			return nil, &BDError{Op: "dep", Message: "dependency row lacks an id, a status or a type: " + truncate(out), Kind: ErrMalformed}
		}
		deps = append(deps, Dependency{ID: r.ID, Kind: depKind(r.Kind), Status: r.Status})
	}
	return deps, nil
}

// config reads the effective configuration, as `bd config show` reports it.
func (b *BD) config(ctx context.Context) (map[string]string, error) {
	out, err := b.run(ctx, "config", "show", "--json")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decode("config", out, &rows); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}

// metadataJSON encodes sets as strings and unsets as null, in one object.
func metadataJSON(set map[string]string, unset []string) (string, error) {
	m := make(map[string]any, len(set)+len(unset))
	for k, v := range set {
		m[k] = v
	}
	for _, k := range unset {
		if _, dup := set[k]; dup {
			return "", fmt.Errorf("%w: metadata key %q is both set and unset", ErrInvalid, k)
		}
		m[k] = nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return string(b), nil
}

// parseJournal reads JSON lines of journal records. A line that is not a
// record is malformed output, and the whole read fails.
func parseJournal(out []byte) ([]Event, error) {
	var evs []Event
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	var last int64
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var w wireEvent
		if err := json.Unmarshal(line, &w); err != nil {
			return nil, &BDError{Op: "events", Message: "journal line is not JSON: " + truncate(line), Kind: ErrMalformed, Cause: err}
		}
		if w.Seq <= last || w.Op == "" || w.IssueID == "" {
			return nil, &BDError{Op: "events", Message: "journal record out of order or incomplete: " + truncate(line), Kind: ErrMalformed}
		}
		last = w.Seq
		e := Event{Seq: w.Seq, TS: w.TS, Op: w.Op, IssueID: w.IssueID, Actor: w.Actor}
		if w.Issue != nil {
			is := w.Issue.issue()
			e.Issue = &is
		}
		evs = append(evs, e)
	}
	if err := sc.Err(); err != nil {
		return nil, &BDError{Op: "events", Message: err.Error(), Kind: ErrMalformed, Cause: err}
	}
	return evs, nil
}
