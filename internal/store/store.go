// Package store keeps Condoo's items in SQLite. The table matches the original todo.py
// store, so an existing database can be pointed at and used as it is.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	Statuses   = []string{"discussion", "in_progress", "up_next", "completed", "cancelled"}
	Priorities = []string{"urgent", "high", "normal", "low"}
	Kinds      = []string{"request", "task", "question"}
	// Fields that `set` may change.
	Fields = []string{"title", "description", "status", "priority", "waiting_on", "agent", "terminal",
		"kind", "parent_id", "answer", "summary", "space", "space_name"}
)

// Task is one item, as JSON for the panel and agents.
type Task struct {
	ID          int64   `json:"id"`
	ParentID    *int64  `json:"parent_id"`
	Kind        string  `json:"kind"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	WaitingOn   string  `json:"waiting_on"`
	Agent       *string `json:"agent"`
	Terminal    *string `json:"terminal"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
	StatusSince float64 `json:"status_since"`
	Answer      *string `json:"answer"`
	Summary     string  `json:"summary"`
	Space       *string `json:"space"`
	// SpaceName is the space's name when last seen, so items can follow a space that's
	// closed and opened again (a new id, the same name).
	SpaceName *string `json:"space_name"`
	// SeenAt is when the person last opened the item in the panel: agents' comments after it
	// are unread.
	SeenAt   *float64  `json:"seen_at"`
	Comments []Comment `json:"comments"` // oldest first
}

// Comment is one note on an item, by the person or an agent.
type Comment struct {
	ID        int64   `json:"id"`
	TaskID    int64   `json:"task_id"`
	Author    string  `json:"author"`
	ByAgent   bool    `json:"by_agent"`
	Body      string  `json:"body"`
	CreatedAt float64 `json:"created_at"`
}

// NewTask is what add/ask/request create.
type NewTask struct {
	Kind        string
	ParentID    *int64
	Title       string
	Summary     string
	Description string
	Status      string // default: up_next for tasks, discussion otherwise
	Priority    string // default: normal
	WaitingOn   string
	Agent       *string
	Terminal    *string
	Space       *string
	SpaceName   *string
}

// ErrNotFound: no item with that id.
type ErrNotFound struct{ ID int64 }

func (e ErrNotFound) Error() string { return fmt.Sprintf("no task %d", e.ID) }

// Store is an open database.
type Store struct {
	db  *sql.DB
	Now func() time.Time
}

// Open creates the folder and the file as needed, and brings the schema up to date by
// running the migrations the database hasn't had.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Pragmas in the DSN apply to every pooled connection (foreign keys are per connection).
	// A plain path (not a file: URI), so spaces (macOS "Application Support") and Windows
	// drive letters need no escaping. A '?' in the path would end it, so refuse one.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("database path can't contain '?': %s", path)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// A newer condoo's database is refused before anything here touches its schema.
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if version > len(migrations) {
		db.Close()
		return nil, fmt.Errorf("%s: database is at schema version %d, but this condoo knows up to %d: upgrade condoo", path, version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		if err := runMigration(db, v+1, migrations[v]); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: migration %d: %w", path, v+1, err)
		}
	}
	return &Store{db: db, Now: time.Now}, nil
}

// schemaV1 is the schema as it stood when versions began to be recorded (migration 1).
const schemaV1 = `
CREATE TABLE IF NOT EXISTS tasks (
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
  seen_at      REAL
);
CREATE INDEX IF NOT EXISTS tasks_parent ON tasks(parent_id);
CREATE TABLE IF NOT EXISTS comments (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  author     TEXT NOT NULL DEFAULT '',
  by_agent   INTEGER NOT NULL DEFAULT 0,
  body       TEXT NOT NULL DEFAULT '',
  created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_task ON comments(task_id);
`

// migrations[i] takes the database from schema version i to i+1. Append new steps; never
// edit or reorder old ones, and write each against the schema the step before it left.
var migrations = []func(tx *sql.Tx) error{
	// 1: the schema, and items nobody has opened show as new. A database from before
	// versions were recorded (back to the original todo.py) may lack the comments table and
	// any column added since, so each is created or added only when missing. Everything
	// from before then counts as seen, so upgrading lights nothing up. Their unread
	// comments stay unread.
	func(tx *sql.Tx) error {
		if _, err := tx.Exec(schemaV1); err != nil {
			return err
		}
		rows, err := tx.Query("PRAGMA table_info(tasks)")
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for rows.Next() {
			var (
				cid, notnull, pk int
				name, typ        string
				dflt             sql.NullString
			)
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			have[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, add := range []struct{ col, ddl string }{
			{"answer", "ALTER TABLE tasks ADD COLUMN answer TEXT"},
			{"summary", "ALTER TABLE tasks ADD COLUMN summary TEXT NOT NULL DEFAULT ''"},
			{"space", "ALTER TABLE tasks ADD COLUMN space TEXT"},
			{"space_name", "ALTER TABLE tasks ADD COLUMN space_name TEXT"},
			{"seen_at", "ALTER TABLE tasks ADD COLUMN seen_at REAL"},
		} {
			if !have[add.col] {
				if _, err := tx.Exec(add.ddl); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec("UPDATE tasks SET seen_at = created_at WHERE seen_at IS NULL")
		return err
	},
}

// runMigration applies one step and records its version in one transaction, so a failed
// step leaves the database at the version before it and runs again on the next start.
func runMigration(db *sql.DB, version int, step func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := step(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) now() float64 { return float64(s.Now().UnixNano()) / 1e9 }

// Check validates the fields with a fixed set of values.
func Check(field, value string) error {
	var allowed []string
	switch field {
	case "status":
		allowed = Statuses
	case "priority":
		allowed = Priorities
	case "kind":
		allowed = Kinds
	default:
		return nil
	}
	if !slices.Contains(allowed, value) {
		return fmt.Errorf("%s must be one of %s", field, strings.Join(allowed, ", "))
	}
	return nil
}

const columns = "id, parent_id, kind, title, description, status, priority, waiting_on, agent, terminal," +
	" created_at, updated_at, status_since, answer, summary, space, space_name, seen_at"

type scanner interface{ Scan(...any) error }

func scan(r scanner) (*Task, error) {
	var (
		t                         Task
		parent                    sql.NullInt64
		agent, term, answer, spce sql.NullString
		spaceName                 sql.NullString
		summary, waiting          sql.NullString
		seen                      sql.NullFloat64
	)
	err := r.Scan(&t.ID, &parent, &t.Kind, &t.Title, &t.Description, &t.Status, &t.Priority, &waiting,
		&agent, &term, &t.CreatedAt, &t.UpdatedAt, &t.StatusSince, &answer, &summary, &spce, &spaceName, &seen)
	if err != nil {
		return nil, err
	}
	if parent.Valid {
		t.ParentID = &parent.Int64
	}
	t.Agent, t.Terminal, t.Answer, t.Space, t.SpaceName = ptr(agent), ptr(term), ptr(answer), ptr(spce), ptr(spaceName)
	t.Summary, t.WaitingOn = summary.String, waiting.String
	if seen.Valid {
		t.SeenAt = &seen.Float64
	}
	t.Comments = []Comment{}
	return &t, nil
}

func ptr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

// Get returns one item.
func (s *Store) Get(id int64) (*Task, error) {
	t, err := scan(s.db.QueryRow("SELECT "+columns+" FROM tasks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound{id}
	}
	if err != nil {
		return nil, err
	}
	byTask, err := s.comments("WHERE task_id = ?", id)
	if err != nil {
		return nil, err
	}
	t.Comments = append(t.Comments, byTask[id]...)
	return t, nil
}

// comments returns the matching comments by item, oldest first.
func (s *Store) comments(where string, args ...any) (map[int64][]Comment, error) {
	rows, err := s.db.Query("SELECT id, task_id, author, by_agent, body, created_at FROM comments "+where+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Author, &c.ByAgent, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out[c.TaskID] = append(out[c.TaskID], c)
	}
	return out, rows.Err()
}

// List returns every item (of one kind, if given), oldest first.
func (s *Store) List(kind string) ([]Task, error) {
	q, args := "SELECT "+columns+" FROM tasks", []any{}
	if kind != "" {
		if err := Check("kind", kind); err != nil {
			return nil, err
		}
		q, args = q+" WHERE kind = ?", append(args, kind)
	}
	rows, err := s.db.Query(q+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byTask, err := s.comments("")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Comments = append(out[i].Comments, byTask[out[i].ID]...)
	}
	return out, nil
}

// Add creates an item. A sub-item takes its parent's kind, and its space unless given.
func (s *Store) Add(n NewTask) (*Task, error) {
	if n.Kind == "" {
		n.Kind = "task"
	}
	if n.ParentID != nil {
		p, err := s.Get(*n.ParentID)
		if err != nil {
			return nil, err
		}
		n.Kind = p.Kind
		if n.Space == nil {
			n.Space, n.SpaceName = p.Space, p.SpaceName
		}
	}
	if n.Status == "" {
		n.Status = "discussion"
		if n.Kind == "task" {
			n.Status = "up_next"
		}
	}
	if n.Priority == "" {
		n.Priority = "normal"
	}
	for _, f := range [][2]string{{"kind", n.Kind}, {"status", n.Status}, {"priority", n.Priority}} {
		if err := Check(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if n.Space == nil || *n.Space == "" {
		n.Space, n.SpaceName = nil, nil // no space: no name either
	}
	now := s.now()
	res, err := s.db.Exec("INSERT INTO tasks (parent_id, kind, title, summary, description, status, priority,"+
		" waiting_on, agent, terminal, space, space_name, created_at, updated_at, status_since)"+
		" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		n.ParentID, n.Kind, n.Title, n.Summary, n.Description, n.Status, n.Priority, n.WaitingOn,
		n.Agent, n.Terminal, n.Space, n.SpaceName, now, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.Get(id)
}

// Update changes fields. Unknown fields and nil values are ignored; a status change resets
// status_since.
func (s *Store) Update(id int64, changes map[string]any) (*Task, error) {
	old, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	var (
		sets []string
		vals []any
	)
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := changes[k]
		if !slices.Contains(Fields, k) || v == nil {
			continue
		}
		if str, ok := v.(string); ok {
			if err := Check(k, str); err != nil {
				return nil, err
			}
		} else if k != "parent_id" {
			return nil, fmt.Errorf("%s must be a string", k)
		}
		sets = append(sets, k+" = ?")
		vals = append(vals, v)
	}
	now := s.now()
	if st, ok := changes["status"].(string); ok && st != old.Status {
		sets = append(sets, "status_since = ?")
		vals = append(vals, now)
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		vals = append(vals, now, id)
		if _, err := s.db.Exec("UPDATE tasks SET "+strings.Join(sets, ", ")+" WHERE id = ?", vals...); err != nil {
			return nil, err
		}
	}
	return s.Get(id)
}

// Move puts an item and all its sub-items in a space (nil: no space, shown in every space).
// It returns the item.
func (s *Store) Move(id int64, space, name *string) (*Task, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if space == nil || *space == "" {
		space, name = nil, nil
	}
	_, err := s.db.Exec(`WITH RECURSIVE tree(id) AS (
	    SELECT id FROM tasks WHERE id = ?
	    UNION ALL SELECT t.id FROM tasks t JOIN tree ON t.parent_id = tree.id)
	  UPDATE tasks SET space = ?, space_name = ?, updated_at = ? WHERE id IN (SELECT id FROM tree)`,
		id, space, name, s.now())
	if err != nil {
		return nil, err
	}
	return s.Get(id)
}

// Delete removes an item and its sub-items.
func (s *Store) Delete(id int64) error {
	_, err := s.db.Exec("DELETE FROM tasks WHERE id = ?", id)
	return err
}

// AddComment adds a note to an item. It doesn't change the item's updated_at.
func (s *Store) AddComment(taskID int64, author string, byAgent bool, body string) (*Comment, error) {
	if _, err := s.Get(taskID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("a comment can't be empty")
	}
	c := Comment{TaskID: taskID, Author: author, ByAgent: byAgent, Body: body, CreatedAt: s.now()}
	res, err := s.db.Exec("INSERT INTO comments (task_id, author, by_agent, body, created_at) VALUES (?,?,?,?,?)",
		c.TaskID, c.Author, c.ByAgent, c.Body, c.CreatedAt)
	if err != nil {
		return nil, err
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Seen records that the person opened an item now, so its comments so far are read.
func (s *Store) Seen(id int64) (*Task, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec("UPDATE tasks SET seen_at = ? WHERE id = ?", s.now(), id); err != nil {
		return nil, err
	}
	return s.Get(id)
}
