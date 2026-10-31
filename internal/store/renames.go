package store

import (
	"context"
	"fmt"
	"strings"
)

// RenameStages sets each column named stage that holds an old name in
// renames to its new name, in one transaction. Where a key that holds the
// stage already has a row under the new name, the row under the new name
// stays and the row under the old name goes, so a start never fails on a
// key that both names hold. It returns the count of rows it moved or
// deleted. A second call with the same renames changes nothing. It reads
// the tables from the schema, so a table that gains a stage column later
// needs no change here.
func (s *Store) RenameStages(ctx context.Context, renames [][2]string) (int, error) {
	if len(renames) == 0 {
		return 0, nil
	}
	n := 0
	err := s.Write(ctx, func(tx *Tx) error {
		tables, err := stageTables(tx)
		if err != nil {
			return err
		}
		for _, table := range tables {
			for _, r := range renames {
				moved, err := execCount(tx, `UPDATE OR IGNORE `+quoteIdent(table)+` SET stage = ? WHERE stage = ?`, r[1], r[0])
				if err != nil {
					return fmt.Errorf("store: rename stage %s to %s in %s: %w", r[0], r[1], table, err)
				}
				// The rows left under the old name are those whose key
				// already holds a row under the new name.
				deleted, err := execCount(tx, `DELETE FROM `+quoteIdent(table)+` WHERE stage = ?`, r[0])
				if err != nil {
					return fmt.Errorf("store: delete stage %s from %s: %w", r[0], table, err)
				}
				n += moved + deleted
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// execCount runs one statement and returns the count of rows it changed.
func execCount(tx *Tx, query string, args ...any) (int, error) {
	res, err := tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	c, err := res.RowsAffected()
	return int(c), err
}

// stageTables lists each table with a column named stage.
func stageTables(tx *Tx) ([]string, error) {
	rows, err := tx.Query(`SELECT m.name FROM sqlite_master m
		WHERE m.type = 'table' AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) p WHERE p.name = 'stage')
		ORDER BY m.name`)
	if err != nil {
		return nil, fmt.Errorf("store: list the tables with a stage column: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// quoteIdent quotes a name that the schema holds for SQL text.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
