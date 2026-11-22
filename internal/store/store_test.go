package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub dir", "condoo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	clock := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { clock = clock.Add(time.Second); return clock }
	return s
}

func str(s string) *string { return &s }

func TestAddDefaults(t *testing.T) {
	s := open(t)
	task, err := s.Add(NewTask{Title: "mine", WaitingOn: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != "task" || task.Status != "up_next" || task.Priority != "normal" || task.WaitingOn != "Ada" {
		t.Errorf("task defaults: %+v", task)
	}
	q, _ := s.Add(NewTask{Kind: "question", Title: "q?", Agent: str("Claude")})
	if q.Status != "discussion" {
		t.Errorf("questions start in discussion, got %s", q.Status)
	}
	if q.CreatedAt != q.UpdatedAt || q.CreatedAt != q.StatusSince || q.CreatedAt == 0 {
		t.Errorf("timestamps: %+v", q)
	}
}

func TestAddValidates(t *testing.T) {
	s := open(t)
	for _, n := range []NewTask{
		{Kind: "nope", Title: "x"},
		{Title: "x", Status: "done"},
		{Title: "x", Priority: "meh"},
	} {
		if _, err := s.Add(n); err == nil {
			t.Errorf("accepted %+v", n)
		}
	}
}

func TestEmptySpaceIsNull(t *testing.T) {
	s := open(t)
	task, _ := s.Add(NewTask{Title: "x", Space: str("")})
	if task.Space != nil {
		t.Errorf("space = %q, want null", *task.Space)
	}
}

func TestSubItemTakesParentKindAndSpace(t *testing.T) {
	s := open(t)
	parent, _ := s.Add(NewTask{Kind: "request", Title: "p", Space: str("s1")})
	child, err := s.Add(NewTask{Kind: "task", Title: "c", ParentID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if child.Kind != "request" || child.Space == nil || *child.Space != "s1" || *child.ParentID != parent.ID {
		t.Errorf("child: %+v", child)
	}
	other, _ := s.Add(NewTask{Title: "c2", ParentID: &parent.ID, Space: str("s2")})
	if *other.Space != "s2" {
		t.Errorf("an explicit space wins, got %s", *other.Space)
	}
	missing := int64(999)
	if _, err := s.Add(NewTask{Title: "orphan", ParentID: &missing}); !errors.As(err, &ErrNotFound{}) {
		t.Errorf("missing parent: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	s := open(t)
	task, _ := s.Add(NewTask{Title: "x"})

	same, err := s.Update(task.ID, map[string]any{"status": "up_next", "nope": "ignored", "summary": nil})
	if err != nil {
		t.Fatal(err)
	}
	if same.StatusSince != task.StatusSince {
		t.Error("an unchanged status must keep status_since")
	}

	moved, _ := s.Update(task.ID, map[string]any{"status": "in_progress", "title": "y"})
	if moved.Status != "in_progress" || moved.Title != "y" || moved.StatusSince <= task.StatusSince || moved.UpdatedAt <= task.UpdatedAt {
		t.Errorf("after update: %+v", moved)
	}

	if _, err := s.Update(task.ID, map[string]any{"priority": "meh"}); err == nil {
		t.Error("accepted a bad priority")
	}
	if _, err := s.Update(task.ID, map[string]any{"title": 3}); err == nil {
		t.Error("accepted a number for title")
	}
	if _, err := s.Update(42, map[string]any{"title": "z"}); !errors.As(err, &ErrNotFound{}) {
		t.Errorf("missing: %v", err)
	}
}

func TestUpdateNothingKeepsUpdatedAt(t *testing.T) {
	s := open(t)
	task, _ := s.Add(NewTask{Title: "x"})
	same, _ := s.Update(task.ID, map[string]any{})
	if same.UpdatedAt != task.UpdatedAt {
		t.Error("no changes must not touch updated_at")
	}
}

func TestDeleteCascades(t *testing.T) {
	s := open(t)
	p, _ := s.Add(NewTask{Title: "p"})
	c, _ := s.Add(NewTask{Title: "c", ParentID: &p.ID})
	gc, _ := s.Add(NewTask{Title: "gc", ParentID: &c.ID})
	keep, _ := s.Add(NewTask{Title: "keep"})
	if err := s.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{p.ID, c.ID, gc.ID} {
		if _, err := s.Get(id); err == nil {
			t.Errorf("item %d survived", id)
		}
	}
	if _, err := s.Get(keep.ID); err != nil {
		t.Error("an unrelated item was deleted")
	}
}

func TestListByKind(t *testing.T) {
	s := open(t)
	s.Add(NewTask{Title: "a"})
	s.Add(NewTask{Kind: "question", Title: "b"})
	s.Add(NewTask{Title: "c"})
	all, _ := s.List("")
	tasks, _ := s.List("task")
	if len(all) != 3 || len(tasks) != 2 || tasks[0].Title != "a" || tasks[1].Title != "c" {
		t.Errorf("all=%d tasks=%v", len(all), tasks)
	}
	if _, err := s.List("bogus"); err == nil {
		t.Error("accepted a bad kind")
	}
	empty := open(t)
	none, _ := empty.List("")
	if none == nil {
		t.Error("an empty list must be [] in JSON, not null")
	}
}

// A database made by the original todo.py, before answer/summary/space existed, opens and
// gains them.
func TestMigratesOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE tasks (
	  id INTEGER PRIMARY KEY AUTOINCREMENT,
	  parent_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
	  kind TEXT NOT NULL DEFAULT 'task', title TEXT NOT NULL DEFAULT '',
	  description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'up_next',
	  priority TEXT NOT NULL DEFAULT 'normal', waiting_on TEXT NOT NULL DEFAULT 'Nigel',
	  agent TEXT, terminal TEXT, created_at REAL NOT NULL, updated_at REAL NOT NULL,
	  status_since REAL NOT NULL);
	INSERT INTO tasks (title, created_at, updated_at, status_since) VALUES ('old one', 1, 1, 1);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Title != "old one" || old.Summary != "" || old.Answer != nil || old.Space != nil || old.SpaceName != nil ||
		old.SeenAt == nil || *old.SeenAt != 1 || old.Comments == nil || len(old.Comments) != 0 {
		t.Errorf("migrated row: %+v", old)
	}
	if _, err := s.Update(1, map[string]any{"answer": "yes", "space": "s1", "space_name": "core", "summary": "sum"}); err != nil {
		t.Fatal(err)
	}
	// Items added after the upgrade start unseen, and reopening doesn't mark them seen.
	added, err := s.Add(NewTask{Kind: "question", Title: "new one", Agent: str("Claude")})
	if err != nil {
		t.Fatal(err)
	}
	// Reopening an up-to-date database changes nothing.
	s.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Get(added.ID); err != nil || got.SeenAt != nil {
		t.Errorf("item added after the upgrade: %+v, %v", got, err)
	}
}

// An upgraded database from before versions were recorded ends at the same schema as a
// fresh one, so later migrations can be written against one schema. Defaults aren't
// compared: todo.py's waiting_on default ('Nigel') was never migrated.
func TestUpgradedDatabaseMatchesFreshSchema(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "fresh.sqlite")
	s, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	upgraded := filepath.Join(t.TempDir(), "todo.sqlite")
	db, err := sql.Open("sqlite", upgraded)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE tasks (
	  id INTEGER PRIMARY KEY AUTOINCREMENT,
	  parent_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
	  kind TEXT NOT NULL DEFAULT 'task', title TEXT NOT NULL DEFAULT '',
	  description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'up_next',
	  priority TEXT NOT NULL DEFAULT 'normal', waiting_on TEXT NOT NULL DEFAULT 'Nigel',
	  agent TEXT, terminal TEXT, created_at REAL NOT NULL, updated_at REAL NOT NULL,
	  status_since REAL NOT NULL)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	want, got := schemaShape(t, fresh), schemaShape(t, upgraded)
	if got != want {
		t.Errorf("upgraded schema differs from a fresh one:\nfresh:\n%s\nupgraded:\n%s", want, got)
	}
}

// schemaShape is each table's columns in order (name, type, not null, primary key), each
// index by name, and the schema version.
func schemaShape(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("version %d\n", version)
	rows, err := db.Query(`SELECT m.type, m.name, p.name, p.type, p."notnull", p.pk
	  FROM sqlite_master m LEFT JOIN pragma_table_info(m.name) p
	  WHERE m.name NOT LIKE 'sqlite_%' ORDER BY m.type, m.name, p.cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		var col, colType sql.NullString
		var notnull, pk sql.NullInt64
		if err := rows.Scan(&typ, &name, &col, &colType, &notnull, &pk); err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%s %s %s %s %d %d\n", typ, name, col.String, colType.String, notnull.Int64, pk.Int64)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A database at the current version runs no migration on open: its schema and its unseen
// items stay as they are.
func TestCurrentDatabaseRunsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	unseen, err := s.Add(NewTask{Kind: "question", Title: "unseen", Agent: str("Claude")})
	if err != nil {
		t.Fatal(err)
	}
	// Migration 1 would create this index again and mark the item seen.
	if _, err := s.db.Exec("DROP INDEX tasks_parent"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before := schemaSQL(t, path)

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if after := schemaSQL(t, path); after != before {
		t.Errorf("opening changed the schema:\nbefore: %s\nafter:  %s", before, after)
	}
	if got, err := s.Get(unseen.ID); err != nil || got.SeenAt != nil {
		t.Errorf("opening changed an unseen item: %+v, %v", got, err)
	}
}

// A database from a newer condoo is refused rather than used with a schema this one
// doesn't know.
func TestRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err == nil {
		s.Close()
		t.Fatal("opened a database from a newer condoo")
	}
	if !errors.Is(err, errNewerSchema) {
		t.Errorf("refused for another reason: %v", err)
	}
}

// A newer condoo may have renamed or dropped columns this one would add back. Refusing
// it must leave its schema exactly as the newer condoo left it.
func TestRefusingNewerDatabaseLeavesSchemaUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE tasks RENAME COLUMN space_name TO space_label"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before := schemaSQL(t, path)

	s, err = Open(path)
	if err == nil {
		s.Close()
		t.Fatal("opened a database from a newer condoo")
	}
	if !errors.Is(err, errNewerSchema) {
		t.Errorf("refused for another reason: %v", err)
	}
	if after := schemaSQL(t, path); after != before {
		t.Errorf("refusing changed the schema:\nbefore: %s\nafter:  %s", before, after)
	}
}

// schemaSQL is every table and index definition in the database at path.
func schemaSQL(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT type, name, sql FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out string
	for rows.Next() {
		var typ, name string
		var def sql.NullString
		if err := rows.Scan(&typ, &name, &def); err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%s %s: %s\n", typ, name, def.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A failing step leaves the database at the version before it, with none of its changes.
func TestFailedMigrationRollsBack(t *testing.T) {
	s := open(t)
	if _, err := s.Add(NewTask{Title: "keep"}); err != nil {
		t.Fatal(err)
	}
	step := func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE tasks SET title = 'changed'"); err != nil {
			return err
		}
		return errors.New("boom")
	}
	if err := runMigration(s.db, len(migrations)+1, migration{run: step}); err == nil {
		t.Fatal("a failing step reported success")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("version after a failed step: %d, %v", version, err)
	}
	if got, err := s.Get(1); err != nil || got.Title != "keep" {
		t.Errorf("a failed step's change was kept: %+v, %v", got, err)
	}
}

// A migration whose commit fails is reported as failed, and leaves the version and its
// row as they were. The step itself succeeds; a deferred foreign key it breaks fails the
// commit. This relies on the driver rolling back after a failed COMMIT (modernc's tx.go
// does), which could only make the test fail wrongly, never pass wrongly. It doesn't prove
// the version write is inside the transaction: writing it after the commit would pass too.
func TestFailedCommitKeepsVersion(t *testing.T) {
	s := open(t)
	step := func(tx *sql.Tx) error {
		if _, err := tx.Exec("PRAGMA defer_foreign_keys = ON"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO comments (task_id, created_at) VALUES (999, 1)")
		return err
	}
	if err := runMigration(s.db, len(migrations)+1, migration{run: step}); err == nil {
		t.Fatal("a commit breaking a foreign key reported success")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("version after a failed commit: %d, %v", version, err)
	}
	var comments int
	if err := s.db.QueryRow("SELECT count(*) FROM comments").Scan(&comments); err != nil || comments != 0 {
		t.Errorf("a failed commit's row was kept: %d, %v", comments, err)
	}
}

// A newer condoo may upgrade the database while this one waits for the write lock, after
// Open's own version check has passed. The step must then refuse, not skip as if done.
func TestMigrationRefusesVersionUpgradedPastKnown(t *testing.T) {
	s := open(t)
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	ran := false
	step := func(tx *sql.Tx) error {
		ran = true
		return nil
	}
	err := runMigration(s.db, len(migrations), migration{run: step})
	if !errors.Is(err, errNewerSchema) {
		t.Errorf("a database upgraded past this condoo wasn't refused: %v", err)
	}
	if ran {
		t.Error("the step ran on a database from a newer condoo")
	}
}

// Several condoo processes may start at once on a database that needs upgrading (agents
// running commands side by side). Each must open it, not fail on the lock another holds.
func TestConcurrentOpensUpgradeOnce(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "fresh.sqlite")
	s, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	want := schemaShape(t, fresh)

	for round := range 5 {
		path := filepath.Join(t.TempDir(), "todo.sqlite")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE tasks (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  parent_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
		  kind TEXT NOT NULL DEFAULT 'task', title TEXT NOT NULL DEFAULT '',
		  description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'up_next',
		  priority TEXT NOT NULL DEFAULT 'normal', waiting_on TEXT NOT NULL DEFAULT 'Nigel',
		  agent TEXT, terminal TEXT, created_at REAL NOT NULL, updated_at REAL NOT NULL,
		  status_since REAL NOT NULL);
		INSERT INTO tasks (title, created_at, updated_at, status_since) VALUES ('old one', 1, 1, 1);`)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}

		errs := openConcurrently(path, 4)
		for i, err := range errs {
			if err != nil {
				t.Errorf("round %d, open %d: %v", round, i, err)
			}
		}
		if got := schemaShape(t, path); got != want {
			t.Errorf("round %d: schema after concurrent upgrades differs from a fresh one:\nfresh:\n%s\nupgraded:\n%s", round, want, got)
		}
	}
}

// openConcurrently opens path from n goroutines at once, each with its own connection
// pool as separate processes would have, and returns each Open's error.
func openConcurrently(path string, n int) []error {
	errs := make([]error, n)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range n {
		done.Go(func() {
			start.Wait()
			s, err := Open(path)
			if err == nil {
				s.Close()
			}
			errs[i] = err
		})
	}
	start.Done()
	done.Wait()
	return errs
}

// A step another process applied while this one waited for the lock isn't applied again:
// a step that isn't idempotent (adding a column) would fail the second time.
func TestConcurrentOpensRunEachStepOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	saved := migrations
	t.Cleanup(func() { migrations = saved })
	migrations = append(slices.Clip(saved), migration{run: func(tx *sql.Tx) error {
		_, err := tx.Exec("ALTER TABLE tasks ADD COLUMN probe TEXT")
		return err
	}})

	for i, err := range openConcurrently(path, 4) {
		if err != nil {
			t.Errorf("open %d: %v", i, err)
		}
	}
	if got := schemaShape(t, path); !strings.Contains(got, fmt.Sprintf("version %d\n", len(migrations))) ||
		!strings.Contains(got, "table tasks probe TEXT") {
		t.Errorf("schema after concurrent upgrades:\n%s", got)
	}
}

// rebuildTasks is SQLite's way to make a change ALTER TABLE can't (a column's constraint,
// default or type): copy tasks into a new table, drop the old one and rename the new one.
func rebuildTasks(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE tasks_new (
		  id           INTEGER PRIMARY KEY AUTOINCREMENT,
		  parent_id    INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
		  kind         TEXT NOT NULL DEFAULT 'task',
		  title        TEXT NOT NULL DEFAULT '',
		  description  TEXT NOT NULL DEFAULT '',
		  status       TEXT NOT NULL DEFAULT 'up_next',
		  priority     TEXT NOT NULL DEFAULT 'normal',
		  waiting_on   TEXT NOT NULL DEFAULT '',
		  agent        TEXT,
		  terminal     TEXT,
		  created_at   REAL NOT NULL,
		  updated_at   REAL NOT NULL,
		  status_since REAL NOT NULL,
		  answer       TEXT,
		  summary      TEXT NOT NULL DEFAULT '',
		  space        TEXT,
		  space_name   TEXT,
		  seen_at      REAL)`,
		"INSERT INTO tasks_new SELECT * FROM tasks",
		"DROP TABLE tasks",
		"ALTER TABLE tasks_new RENAME TO tasks",
		"CREATE INDEX tasks_parent ON tasks(parent_id)",
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func countComments(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM comments").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func foreignKeysOn(t *testing.T, s *Store) bool {
	t.Helper()
	var on bool
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatal(err)
	}
	return on
}

// Dropping a table that other tables reference deletes their rows through ON DELETE
// CASCADE while foreign keys are on, and SQLite can't turn them off inside the step's
// transaction. A step marked foreignKeysOff rebuilds tasks and keeps every comment, and
// the references still work afterwards.
func TestMigrationWithForeignKeysOffKeepsReferencingRows(t *testing.T) {
	t.Run("foreign keys on: the rebuild deletes the comments", func(t *testing.T) {
		s := open(t)
		task, err := s.Add(NewTask{Title: "parent"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddComment(task.ID, "Claude", true, "keep me"); err != nil {
			t.Fatal(err)
		}
		if err := runMigration(s.db, len(migrations)+1, migration{run: rebuildTasks}); err != nil {
			t.Fatal(err)
		}
		if n := countComments(t, s); n != 0 {
			t.Errorf("expected the cascade this option exists for; comments left: %d", n)
		}
	})

	t.Run("foreign keys off: the rebuild keeps them", func(t *testing.T) {
		s := open(t)
		task, err := s.Add(NewTask{Title: "parent"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddComment(task.ID, "Claude", true, "keep me"); err != nil {
			t.Fatal(err)
		}
		if err := runMigration(s.db, len(migrations)+1, migration{foreignKeysOff: true, run: rebuildTasks}); err != nil {
			t.Fatal(err)
		}
		if n := countComments(t, s); n != 1 {
			t.Errorf("the rebuild lost comments: %d left, want 1", n)
		}
		if !foreignKeysOn(t, s) {
			t.Error("foreign keys were left off after the step")
		}
		// The comment still references the rebuilt table: deleting its task cascades.
		if err := s.Delete(task.ID); err != nil {
			t.Fatal(err)
		}
		if n := countComments(t, s); n != 0 {
			t.Errorf("deleting the task after the rebuild left %d comments", n)
		}
	})
}

// With foreign keys off nothing stops a step breaking a reference, so it's checked before
// the commit: a broken one fails the step and leaves the version and the rows as they were.
func TestMigrationWithForeignKeysOffRefusesBrokenReference(t *testing.T) {
	s := open(t)
	step := func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO comments (task_id, created_at) VALUES (999, 1)")
		return err
	}
	if err := runMigration(s.db, len(migrations)+1, migration{foreignKeysOff: true, run: step}); err == nil {
		t.Fatal("a step leaving a broken reference reported success")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("version after a refused step: %d, %v", version, err)
	}
	if n := countComments(t, s); n != 0 {
		t.Errorf("a refused step's row was kept: %d", n)
	}
	if !foreignKeysOn(t, s) {
		t.Error("foreign keys were left off after a refused step")
	}
}

func TestRefusesQuestionMarkInPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "what?.sqlite")); err == nil {
		t.Error("accepted a path with ?")
	}
}

func TestMoveSubtree(t *testing.T) {
	s := open(t)
	p, _ := s.Add(NewTask{Title: "p", Space: str("old"), SpaceName: str("api")})
	c, _ := s.Add(NewTask{Title: "c", ParentID: &p.ID})
	if c.SpaceName == nil || *c.SpaceName != "api" {
		t.Fatalf("sub-item didn't take the parent's space name: %+v", c)
	}
	keep, _ := s.Add(NewTask{Title: "keep", Space: str("old"), SpaceName: str("api")})

	if _, err := s.Move(p.ID, str("new"), str("web")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{p.ID, c.ID} {
		got, _ := s.Get(id)
		if *got.Space != "new" || *got.SpaceName != "web" {
			t.Errorf("%d: %v %v", id, *got.Space, *got.SpaceName)
		}
	}
	if got, _ := s.Get(keep.ID); *got.Space != "old" {
		t.Error("moved an unrelated item")
	}

	// No space clears the name too, even if one is given.
	got, _ := s.Move(p.ID, str(""), str("ignored"))
	if got.Space != nil || got.SpaceName != nil {
		t.Errorf("to no space: %+v", got)
	}
	if _, err := s.Move(999, nil, nil); !errors.As(err, &ErrNotFound{}) {
		t.Errorf("missing: %v", err)
	}
}

func TestNoSpaceMeansNoName(t *testing.T) {
	s := open(t)
	task, _ := s.Add(NewTask{Title: "x", SpaceName: str("orphan name")})
	if task.SpaceName != nil {
		t.Errorf("a name without a space was kept: %v", *task.SpaceName)
	}
}

func TestComments(t *testing.T) {
	s := open(t)
	task, _ := s.Add(NewTask{Title: "x"})
	s.Add(NewTask{Title: "y"})
	if _, err := s.AddComment(task.ID, "Claude", true, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(task.ID, "Ada", false, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(task.ID, "Ada", false, "  \n"); err == nil {
		t.Error("accepted an empty comment")
	}
	if _, err := s.AddComment(42, "Ada", false, "z"); !errors.As(err, &ErrNotFound{}) {
		t.Errorf("missing item: %v", err)
	}

	got, _ := s.Get(task.ID)
	if len(got.Comments) != 2 || got.Comments[0].Body != "first" || !got.Comments[0].ByAgent ||
		got.Comments[1].Author != "Ada" || got.Comments[1].ByAgent {
		t.Errorf("comments: %+v", got.Comments)
	}
	if got.UpdatedAt != task.UpdatedAt {
		t.Error("a comment must not touch updated_at")
	}
	all, _ := s.List("")
	if len(all[0].Comments) != 2 || all[1].Comments == nil || len(all[1].Comments) != 0 {
		t.Errorf("listed: %+v", all)
	}

	seen, err := s.Seen(task.ID)
	if err != nil || seen.SeenAt == nil || *seen.SeenAt <= got.Comments[1].CreatedAt || seen.UpdatedAt != task.UpdatedAt {
		t.Errorf("seen: %+v %v", seen, err)
	}

	if err := s.Delete(task.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM comments").Scan(&n)
	if n != 0 {
		t.Errorf("%d comments outlived their item", n)
	}
}
