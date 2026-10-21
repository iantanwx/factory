package fault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// Resolution is one way a fault ends. Exactly one field holds a value.
type Resolution struct {
	FixedIn     string
	DuplicateOf string
	// Reason says why the report is not a fault.
	Reason string
}

func (r Resolution) status() string {
	switch {
	case r.FixedIn != "":
		return Fixed
	case r.DuplicateOf != "":
		return Duplicate
	}
	return NotAFault
}

var errNotAllowed = errors.New("fault: the move is not allowed")

// ack is `loop fault ack <id>`: an open fault becomes acknowledged. An
// acknowledged fault answers as it is.
func (s *Service) ack(ctx context.Context, call *rpc.Call) cli.Result {
	id := call.Inv.Arg("fault")
	n, ok := ParseID(id)
	if !ok {
		return refuse(cli.CodeUsage, "bad_fault", fmt.Sprintf("%q is no fault id; a fault id is F-<n>", id))
	}
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		status, err := statusOf(tx, n)
		if err != nil {
			return err
		}
		switch status {
		case Acknowledged:
			return nil
		case Open:
			now := tx.Now().UnixMilli()
			_, err := tx.Exec(`UPDATE faults SET status = ?, acknowledged_at = ?, updated_at = ? WHERE n = ?`, Acknowledged, now, now, n)
			return err
		}
		return fmt.Errorf("%w: %s is %s", errNotAllowed, id, status)
	})
	return s.answer(ctx, id, err, "ack")
}

// resolve is `loop fault resolve <id>` with exactly one of --fixed-in,
// --duplicate-of and --not-a-fault --reason.
func (s *Service) resolve(ctx context.Context, call *rpc.Call) cli.Result {
	id, fl := call.Inv.Arg("fault"), call.Inv.Flags
	n, ok := ParseID(id)
	if !ok {
		return refuse(cli.CodeUsage, "bad_fault", fmt.Sprintf("%q is no fault id; a fault id is F-<n>", id))
	}
	r := Resolution{FixedIn: strings.TrimSpace(fl["fixed-in"]), DuplicateOf: strings.TrimSpace(fl["duplicate-of"]),
		Reason: strings.TrimSpace(fl["reason"])}
	notAFault := fl["not-a-fault"] == "true"
	count := 0
	for _, set := range []bool{r.FixedIn != "", r.DuplicateOf != "", notAFault} {
		if set {
			count++
		}
	}
	const one = "name exactly one resolution: --fixed-in <release>, --duplicate-of <id>, or --not-a-fault --reason <text>"
	switch {
	case count != 1:
		return refuse(cli.CodeUsage, "one_resolution", one)
	case notAFault && r.Reason == "":
		return refuse(cli.CodeUsage, "no_reason", "--not-a-fault needs --reason, which says why the report is no fault in the loop")
	case !notAFault && r.Reason != "":
		return refuse(cli.CodeUsage, "reason_without_not_a_fault", "--reason goes with --not-a-fault only; "+one)
	}
	if r.DuplicateOf != "" {
		dn, ok := ParseID(r.DuplicateOf)
		if !ok {
			return refuse(cli.CodeUsage, "bad_duplicate", fmt.Sprintf("--duplicate-of %q is no fault id; a fault id is F-<n>", r.DuplicateOf))
		}
		if dn == n {
			return refuse(cli.CodeUsage, "duplicate_of_itself", "a fault cannot be a duplicate of itself")
		}
		r.DuplicateOf = FormatID(dn)
	}
	if res := s.secretIn("reason", r.Reason); res != nil {
		return *res
	}
	actor := call.Principal.Actor()
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		status, err := statusOf(tx, n)
		if err != nil {
			return err
		}
		if status != Open && status != Acknowledged {
			return fmt.Errorf("%w: %s is already resolved as %s", errNotAllowed, id, status)
		}
		if r.DuplicateOf != "" {
			ok, err := Exists(tx, r.DuplicateOf)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: --duplicate-of %s", ErrNotFound, r.DuplicateOf)
			}
		}
		now := tx.Now().UnixMilli()
		_, err = tx.Exec(`UPDATE faults SET status = ?, fixed_in = ?, duplicate_of = ?, reason = ?, resolved_by = ?, resolved_at = ?,
			updated_at = ? WHERE n = ?`, r.status(), r.FixedIn, r.DuplicateOf, r.Reason, actor, now, now, n)
		return err
	})
	return s.answer(ctx, id, err, "resolve")
}

func statusOf(tx *store.Tx, n int64) (string, error) {
	var status string
	err := tx.QueryRow(`SELECT status FROM faults WHERE n = ?`, n).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, FormatID(n))
	}
	return status, err
}

// answer maps the write's error, and on success shows the fault.
func (s *Service) answer(ctx context.Context, id string, err error, verb string) cli.Result {
	switch {
	case errors.Is(err, ErrNotFound):
		return refuse(cli.CodeNotFound, "no_fault", fmt.Sprintf("the server gave out no fault %s", strings.TrimPrefix(err.Error(), ErrNotFound.Error()+": ")))
	case errors.Is(err, errNotAllowed):
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "fault_" + verb, Item: id,
			Hint: strings.TrimPrefix(err.Error(), errNotAllowed.Error()+": ")})
	case err != nil:
		return internalErr(err)
	}
	f, err := s.Get(ctx, id)
	if err != nil {
		return internalErr(err)
	}
	f.Installed = s.release()
	return cli.Found(f)
}
