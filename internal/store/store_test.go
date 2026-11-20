package store

import (
	"database/sql"
	"errors"
	"path/filepath"
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
