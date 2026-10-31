package store

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// checkValues satisfy the CHECK of each column that holds one, in every
// table with a stage column.
var checkValues = map[string]any{
	"kind":          "run",
	"reporter_kind": "run",
	"choice":        "inside",
	"level":         1,
}

// column is one column of a table, as pragma_table_info reads it.
type column struct {
	name, typ  string
	notNull    bool
	hasDefault bool
	pk         bool
}

// stageColumnTables reads each table with a column named stage, and its
// columns, from the schema.
func stageColumnTables(t *testing.T, s *Store) map[string][]column {
	t.Helper()
	out := map[string][]column{}
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT m.name, p.name, p.type, p."notnull", p.dflt_value IS NOT NULL, p.pk > 0
			FROM sqlite_master m, pragma_table_info(m.name) p
			WHERE m.type = 'table' AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) q WHERE q.name = 'stage')
			ORDER BY m.name, p.cid`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var table string
			var c column
			if err := rows.Scan(&table, &c.name, &c.typ, &c.notNull, &c.hasDefault, &c.pk); err != nil {
				return err
			}
			out[table] = append(out[table], c)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// insertRow adds one row to table with stage set, and a fresh value in
// each other column that needs one. The project of each row is its own,
// so no two rows share a key.
func insertRow(t *testing.T, s *Store, table string, cols []column, stage string, seq int) {
	t.Helper()
	project := fmt.Sprintf("p%d", seq)
	addProject(t, s, project)
	var names []string
	var values []any
	for _, c := range cols {
		var v any
		switch {
		case c.name == "stage":
			v = stage
		case c.name == "project":
			v = project
		case checkValues[c.name] != nil:
			v = checkValues[c.name]
		case c.pk && strings.EqualFold(c.typ, "INTEGER"):
			continue
		case !c.notNull || c.hasDefault:
			continue
		case strings.EqualFold(c.typ, "INTEGER"):
			v = seq
		default:
			v = fmt.Sprintf("%s-%d", c.name, seq)
		}
		names, values = append(names, `"`+c.name+`"`), append(values, v)
	}
	q := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`, table, strings.Join(names, ", "),
		strings.Repeat("?, ", len(names)-1)+"?")
	if err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(q, values...)
		return err
	}); err != nil {
		t.Fatalf("insert into %s: %v", table, err)
	}
}

// dumpTables reads every row of each table, each value as text, in a
// stable order.
func dumpTables(t *testing.T, s *Store, tables []string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		for _, table := range tables {
			rows, err := tx.Query(`SELECT * FROM "` + table + `"`)
			if err != nil {
				return err
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]sql.NullString, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					_ = rows.Close()
					return err
				}
				var b strings.Builder
				for i, c := range cols {
					fmt.Fprintf(&b, "%s=%v/%s;", c, vals[i].Valid, vals[i].String)
				}
				out[table] = append(out[table], b.String())
			}
			_ = rows.Close()
			slices.Sort(out[table])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// C8: after RenameStages no column named stage, in any table, holds an
// old name of the table. Each row keeps every other value, and a second
// call changes no row. The tables come from the schema, so a table that
// gains a stage column later is covered.
func TestC8RenameStagesLeavesNoOldStageName(t *testing.T) {
	renames := [][2]string{{"old-a", "new-a"}, {"old-b", "new-b"}}
	names := []string{"old-a", "new-a", "old-b", "new-b", "implementer"}
	for seed := range uint64(20) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			s := openTemp(t)
			tables := stageColumnTables(t, s)
			if len(tables) < 8 {
				t.Fatalf("the schema holds %d tables with a stage column, want 8 or more", len(tables))
			}
			olds, seq := 0, 0
			tableNames := slices.Sorted(maps.Keys(tables))
			for _, table := range tableNames {
				for range rng.IntN(4) {
					stage := names[rng.IntN(len(names))]
					if strings.HasPrefix(stage, "old-") {
						olds++
					}
					seq++
					insertRow(t, s, table, tables[table], stage, seq)
				}
			}
			before := dumpTables(t, s, tableNames)

			n, err := s.RenameStages(context.Background(), renames)
			if err != nil {
				t.Fatal(err)
			}
			if n != olds {
				t.Fatalf("RenameStages changed %d rows, want %d", n, olds)
			}
			after := dumpTables(t, s, tableNames)
			for _, table := range tableNames {
				want := slices.Clone(before[table])
				for i, row := range want {
					for _, r := range renames {
						row = strings.Replace(row, "stage=true/"+r[0]+";", "stage=true/"+r[1]+";", 1)
					}
					want[i] = row
				}
				slices.Sort(want)
				if !slices.Equal(after[table], want) {
					t.Errorf("%s after the rename:\n%q\nwant:\n%q", table, after[table], want)
				}
				for _, row := range after[table] {
					if strings.Contains(row, "stage=true/old-") {
						t.Errorf("%s still holds an old stage name: %s", table, row)
					}
				}
			}

			again, err := s.RenameStages(context.Background(), renames)
			if err != nil || again != 0 {
				t.Fatalf("a second RenameStages changed %d rows (err %v), want 0", again, err)
			}
		})
	}
}
