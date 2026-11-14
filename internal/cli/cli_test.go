package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NigelGreenway/condoo/internal/config"
)

// harness runs the CLI against a temporary home, database and config.
type harness struct {
	t      *testing.T
	env    map[string]string
	deps   Deps
	spaces []fakeSpace // what the fake `herdr-app ls` reports
	looked int         // how often it was asked
}

func newHarness(t *testing.T) *harness {
	home := t.TempDir()
	h := &harness{t: t, env: map[string]string{
		"CONDOO_DB":      filepath.Join(home, "db", "condoo.sqlite"),
		"CONDOO_CONFIG":  filepath.Join(home, "config.json"),
		"XDG_CACHE_HOME": filepath.Join(home, "cache"),
		"LOCALAPPDATA":   filepath.Join(home, "local"),
	}}
	clock := time.Unix(1_700_000_000, 0)
	h.deps = Deps{
		Sys:      config.System{GOOS: "linux", Home: home, Env: func(k string) string { return h.env[k] }},
		Login:    func() string { return "ada" },
		HerdrLS:  func() []byte { h.looked++; return fakeLS(h.spaces) },
		LookPath: func(string) (string, error) { return "", errors.New("none") },
		Now:      func() time.Time { clock = clock.Add(time.Second); return clock },
		Version:  "test",
	}
	return h
}

// run returns the exit code and the decoded JSON output.
func (h *harness) run(args ...string) (int, map[string]any) {
	h.t.Helper()
	var out bytes.Buffer
	h.deps.Stdout = &out
	code := Run(args, h.deps)
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		h.t.Fatalf("%v: output isn't a JSON object: %q", args, out.String())
	}
	return code, m
}

func (h *harness) ok(args ...string) map[string]any {
	h.t.Helper()
	code, m := h.run(args...)
	if code != 0 {
		h.t.Fatalf("%v: exit %d: %v", args, code, m)
	}
	return m
}

func TestParse(t *testing.T) {
	flags := []flag{
		{key: "summary", names: []string{"-s", "--summary"}},
		{key: "priority", names: []string{"--priority"}, choices: []string{"high", "low"}},
		{key: "space", names: []string{"--space"}},
	}
	pos, opts, err := parse([]string{"-s", "one", "title", "--priority=high", "--space=", "--", "-literal"}, flags)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"title", "-literal"}) {
		t.Errorf("pos = %q", pos)
	}
	if !reflect.DeepEqual(opts, map[string]string{"summary": "one", "priority": "high", "space": ""}) {
		t.Errorf("opts = %q", opts)
	}
	for _, bad := range [][]string{{"--nope", "x"}, {"--summary"}, {"--priority", "mid"}} {
		if _, _, err := parse(bad, flags); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if pos, _, _ := parse([]string{"-"}, flags); len(pos) != 1 {
		t.Error("a lone - is a positional")
	}
}

func TestSplitCommand(t *testing.T) {
	for in, want := range map[string][]string{
		"vim":                               {"vim"},
		"code --wait":                       {"code", "--wait"},
		`  nvim   -u  NONE `:                {"nvim", "-u", "NONE"},
		`"C:\Program Files\Vim\vim.exe" -n`: {`C:\Program Files\Vim\vim.exe`, "-n"},
		`emacsclient -a '' -t`:              {"emacsclient", "-a", "", "-t"},
		"":                                  nil,
	} {
		if got := splitCommand(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAskFromAnAgent(t *testing.T) {
	h := newHarness(t)
	h.env["HC_TERMINAL_ID"] = "t7"
	h.env["CONDOO_AGENT"] = "Claude (TICKET-123)"
	h.spaces = []fakeSpace{{"space-1", "other", nil}, {"space-3", "condoo", []string{"t7"}}}
	q := h.ok("ask", "Which values?", "-s", "short", "-d", "long\ncontext", "--priority", "high")
	want := map[string]any{
		"kind": "question", "title": "Which values?", "summary": "short", "description": "long\ncontext",
		"priority": "high", "status": "discussion", "agent": "Claude (TICKET-123)", "terminal": "t7", "space": "space-3",
		"waiting_on": "ada", "answer": nil, "parent_id": nil, "space_name": "condoo",
	}
	for k, v := range want {
		if q[k] != v {
			t.Errorf("%s = %v, want %v", k, q[k], v)
		}
	}
}

func TestRequestDefaultsAgent(t *testing.T) {
	h := newHarness(t)
	r := h.ok("request", "Approve it")
	if r["kind"] != "request" || r["agent"] != "agent" || r["terminal"] != nil || r["space"] != nil {
		t.Errorf("request: %v", r)
	}
	if h.looked != 0 {
		t.Error("looked up a space without a terminal")
	}
}

func TestAddFromThePanel(t *testing.T) {
	h := newHarness(t)
	h.env["HC_TERMINAL_ID"] = "t1"
	// The panel passes empty --terminal/--agent and an explicit --space.
	task := h.ok("add", "--kind=task", "--summary=", "--terminal=", "--agent=", "--space=", "--", "-starts with a dash")
	if task["title"] != "-starts with a dash" || task["agent"] != "" || task["terminal"] != "" || task["space"] != nil {
		t.Errorf("task: %v", task)
	}
	if h.looked != 0 {
		t.Error("an explicit empty --space must skip the lookup")
	}
	sub := h.ok("add", "child", "--parent", jsonID(task), "--terminal=")
	if sub["parent_id"] != task["id"] {
		t.Errorf("sub: %v", sub)
	}
}

func TestNameComesFromConfig(t *testing.T) {
	h := newHarness(t)
	h.ok("config", "set", "name", "  Ada Lovelace ")
	if q := h.ok("ask", "q"); q["waiting_on"] != "Ada Lovelace" {
		t.Errorf("waiting_on = %v", q["waiting_on"])
	}
	cfg := h.ok("config")
	if cfg["name"] != "Ada Lovelace" || cfg["name_source"] != "config" || cfg["db_source"] != "env" {
		t.Errorf("config: %v", cfg)
	}
	h.ok("config", "set", "name", "")
	if cfg := h.ok("config"); cfg["name"] != "ada" || cfg["name_source"] != "login" {
		t.Errorf("after reset: %v", cfg)
	}
	if code, _ := h.run("config", "set", "colour", "blue"); code != 2 {
		t.Error("accepted an unknown config key")
	}
}

func TestListGetSetDelete(t *testing.T) {
	h := newHarness(t)
	a := h.ok("add", "a")
	h.ok("ask", "b")
	list := h.ok("list")
	if len(list["tasks"].([]any)) != 2 || list["name"] != "ada" || list["db"] != h.env["CONDOO_DB"] {
		t.Errorf("list: %v", list)
	}
	if qs := h.ok("list", "--kind", "question")["tasks"].([]any); len(qs) != 1 {
		t.Errorf("questions: %v", qs)
	}
	id := jsonID(a)
	if got := h.ok("set", id, "--status", "completed", "--title", "A"); got["status"] != "completed" || got["title"] != "A" {
		t.Errorf("set: %v", got)
	}
	if got := h.ok("get", id); got["title"] != "A" {
		t.Errorf("get: %v", got)
	}
	if got := h.ok("delete", id); got["deleted"] != a["id"] {
		t.Errorf("delete: %v", got)
	}
	if code, m := h.run("get", id); code != 1 || m["error"] != "no task "+id {
		t.Errorf("get after delete: %d %v", code, m)
	}
	if code, _ := h.run("delete", id); code != 1 {
		t.Error("deleting a missing item must fail")
	}
}

func TestSetJSON(t *testing.T) {
	h := newHarness(t)
	p := h.ok("add", "p")
	c := h.ok("add", "c")
	got := h.ok("set-json", jsonID(c), `{"parent_id": `+jsonID(p)+`, "summary": "s", "agent": null}`)
	if got["parent_id"] != p["id"] || got["summary"] != "s" {
		t.Errorf("set-json: %v", got)
	}
	if code, _ := h.run("set-json", jsonID(c), `{"parent_id": 1.5}`); code != 2 {
		t.Error("accepted a fractional id")
	}
	if code, _ := h.run("set-json", jsonID(c), `[1]`); code != 2 {
		t.Error("accepted a non-object")
	}
}

func TestUsageErrors(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"frobnicate"},
		{"ask"},
		{"ask", "one", "two"},
		{"ask", "q", "--priority", "whenever"},
		{"get", "twelve"},
		{"add", "x", "--parent", "p"},
	} {
		if code, m := h.run(args...); code != 2 || m["error"] == nil {
			t.Errorf("%q: exit %d, %v", args, code, m)
		}
	}
}

func TestHelpAndVersion(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{nil, {"--help"}, {"ask", "-h"}} {
		var out bytes.Buffer
		h.deps.Stdout = &out
		if code := Run(args, h.deps); code != 0 || !strings.Contains(out.String(), "condoo ask") {
			t.Errorf("%q: %d %q", args, code, out.String())
		}
	}
	if v := h.ok("version"); v["version"] != "test" {
		t.Errorf("version: %v", v)
	}
}

func TestAnswerRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.env["EDITOR"] = "code --wait"
	q := h.ok("ask", "Pick one?", "-s", "A or B", "-d", "line 1\nline 2", "--agent", "Claude")
	id := jsonID(q)

	f := h.ok("answer-file", id)
	path := f["path"].(string)
	if !strings.HasPrefix(path, filepath.Join(h.env["XDG_CACHE_HOME"], "condoo", "answers")) {
		t.Errorf("draft outside the cache folder: %s", path)
	}
	if !reflect.DeepEqual(f["editor"], []any{"code", "--wait"}) {
		t.Errorf("editor: %v", f["editor"])
	}
	draft, _ := os.ReadFile(path)
	for _, want := range []string{Cut, "# Question: Pick one?", "# A or B", "# line 2", "# Asked by: Claude"} {
		if !strings.Contains(string(draft), want) {
			t.Errorf("draft lacks %q:\n%s", want, draft)
		}
	}

	// An untouched draft cancels.
	if got := h.ok("answer-save", id, path); got["cancelled"] != true {
		t.Errorf("empty answer: %v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the draft must be removed")
	}

	// A real answer (Windows line endings too) completes the question.
	h.ok("answer-file", id)
	os.WriteFile(path, []byte("Option B,\r\nbecause.\r\n\r\n"+Cut+"\r\n# Question: Pick one?\r\n"), 0o600)
	got := h.ok("answer-save", id, path)
	if got["answer"] != "Option B,\nbecause." || got["status"] != "completed" || got["waiting_on"] != "Claude" {
		t.Errorf("answered: %v", got)
	}

	// The next draft starts with the answer; saving it unchanged cancels.
	h.ok("answer-file", id)
	if draft, _ := os.ReadFile(path); !strings.HasPrefix(string(draft), "Option B,\nbecause.\n\n") {
		t.Errorf("draft doesn't start with the answer: %q", draft)
	}
	if got := h.ok("answer-save", id, path); got["cancelled"] != true {
		t.Errorf("unchanged: %v", got)
	}
}

func TestEditor(t *testing.T) {
	h := newHarness(t)
	found := map[string]bool{}
	h.deps.LookPath = func(c string) (string, error) {
		if found[c] {
			return "/bin/" + c, nil
		}
		return "", errors.New("no")
	}
	check := func(goos string, want ...string) {
		t.Helper()
		h.deps.Sys.GOOS = goos
		if got := Editor(h.deps); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", goos, got, want)
		}
	}
	check("linux", "vim") // nothing found: the first choice
	found["nano"] = true
	check("linux", "nano")
	check("windows", "nano")
	found["edit"] = true
	check("windows", "edit")
	h.env["EDITOR"] = "hx"
	check("linux", "hx")
	h.env["VISUAL"] = "nvim -p"
	check("windows", "nvim", "-p")
}

func TestSpaceInAndName(t *testing.T) {
	ls := []byte(`{"spaces":[
	  {"id":"s1","name":"core","tabs":[{"terminals":[{"terminal":"t1"}]}]},
	  {"id":"s2","name":"condoo","tabs":[{"terminals":[]},{"terminals":[{"terminal":"t2","sub_agents":[{"terminal":"t9","sub_agents":[{"terminal":"t10"}]}]}]}]}]}`)
	for term, want := range map[string][2]string{
		"t1": {"s1", "core"}, "t2": {"s2", "condoo"}, "t9": {"s2", "condoo"}, "t10": {"s2", "condoo"},
		"t404": {"", ""}, "": {"", ""},
	} {
		if id, name := SpaceIn(ls, term); id != want[0] || name != want[1] {
			t.Errorf("%q: got %q %q, want %q", term, id, name, want)
		}
	}
	if id, _ := SpaceIn([]byte("not json"), "t1"); id != "" {
		t.Error("bad JSON must give no space")
	}
	if SpaceName(ls, "s2") != "condoo" || SpaceName(ls, "gone") != "" || SpaceName(nil, "s1") != "" {
		t.Error("SpaceName")
	}
}

func TestSpaceNameIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.spaces = []fakeSpace{{"sp-a", "api", nil}, {"sp-b", "web", nil}}

	// --space without a name: the name is looked up.
	a := h.ok("add", "a", "--space", "sp-a")
	if a["space"] != "sp-a" || a["space_name"] != "api" {
		t.Errorf("looked-up name: %v", a)
	}
	// --space-name given (the panel): no lookup.
	before := h.looked
	b := h.ok("add", "b", "--space", "sp-b", "--space-name", "web")
	if b["space_name"] != "web" || h.looked != before {
		t.Errorf("given name: %v (lookups %d -> %d)", b, before, h.looked)
	}
	// A space Herdr doesn't know (closed): kept, with no name.
	c := h.ok("add", "c", "--space", "sp-gone")
	if c["space"] != "sp-gone" || c["space_name"] != nil {
		t.Errorf("unknown space: %v", c)
	}
	// Sub-items take the parent's space and name.
	sub := h.ok("add", "sub", "--parent", jsonID(a))
	if sub["space"] != "sp-a" || sub["space_name"] != "api" {
		t.Errorf("sub-item: %v", sub)
	}
	// Herdr not running: still works, without a name.
	h.deps.HerdrLS = func() []byte { return nil }
	d := h.ok("add", "d", "--space", "sp-a")
	if d["space"] != "sp-a" || d["space_name"] != nil {
		t.Errorf("no Herdr: %v", d)
	}
}

func TestMove(t *testing.T) {
	h := newHarness(t)
	h.spaces = []fakeSpace{{"sp-new", "condoo", nil}}
	p := h.ok("add", "parent", "--space", "sp-old", "--space-name", "condoo")
	c := h.ok("add", "child", "--parent", jsonID(p))
	g := h.ok("add", "grandchild", "--parent", jsonID(c))
	other := h.ok("add", "other", "--space", "sp-old", "--space-name", "condoo")

	moved := h.ok("move", jsonID(p), "--space", "sp-new")
	if moved["space"] != "sp-new" || moved["space_name"] != "condoo" {
		t.Errorf("moved: %v", moved)
	}
	for _, it := range []map[string]any{c, g} {
		if got := h.ok("get", jsonID(it)); got["space"] != "sp-new" || got["space_name"] != "condoo" {
			t.Errorf("sub-item not moved: %v", got)
		}
	}
	if got := h.ok("get", jsonID(other)); got["space"] != "sp-old" {
		t.Errorf("an unrelated item moved: %v", got)
	}

	personal := h.ok("move", jsonID(p), "--space", "")
	if personal["space"] != nil || personal["space_name"] != nil {
		t.Errorf("to no space: %v", personal)
	}
	if got := h.ok("get", jsonID(g)); got["space"] != nil {
		t.Errorf("grandchild kept its space: %v", got)
	}
	if code, _ := h.run("move", jsonID(p)); code != 2 {
		t.Error("move without --space must be a usage error")
	}
	if code, _ := h.run("move", "999", "--space", "x"); code != 1 {
		t.Error("moving a missing item must fail")
	}
}

type fakeSpace struct {
	id, name  string
	terminals []string
}

func fakeLS(spaces []fakeSpace) []byte {
	type term struct {
		Terminal string `json:"terminal"`
	}
	type tab struct {
		Terminals []term `json:"terminals"`
	}
	type space struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Tabs []tab  `json:"tabs"`
	}
	doc := struct {
		Spaces []space `json:"spaces"`
	}{Spaces: []space{}}
	for _, sp := range spaces {
		var ts []term
		for _, t := range sp.terminals {
			ts = append(ts, term{t})
		}
		doc.Spaces = append(doc.Spaces, space{sp.id, sp.name, []tab{{ts}}})
	}
	b, _ := json.Marshal(doc)
	return b
}

func jsonID(m map[string]any) string {
	b, _ := json.Marshal(m["id"])
	return string(b)
}

func TestComments(t *testing.T) {
	h := newHarness(t)
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude"))

	c := h.ok("comment", id, "From the agent", "--agent", "Claude (lead)")
	if c["author"] != "Claude (lead)" || c["by_agent"] != true {
		t.Errorf("agent comment: %v", c)
	}
	h.env["CONDOO_AGENT"] = "Codex"
	if c := h.ok("comment", id, "env"); c["author"] != "Codex" || c["by_agent"] != true {
		t.Errorf("$CONDOO_AGENT comment: %v", c)
	}
	if c := h.ok("comment", id, "mine", "--agent", ""); c["author"] != "ada" || c["by_agent"] != false {
		t.Errorf("own comment: %v", c)
	}
	if code, _ := h.run("comment", id, " "); code != 1 {
		t.Errorf("empty comment: exit %d", code)
	}
	if code, _ := h.run("comment", id); code != 2 {
		t.Errorf("no text: exit %d", code)
	}

	got := h.ok("get", id)
	if cs := got["comments"].([]any); len(cs) != 3 || cs[0].(map[string]any)["body"] != "From the agent" {
		t.Errorf("get comments: %v", got["comments"])
	}
	if got["seen_at"] != nil {
		t.Errorf("seen_at before seen: %v", got["seen_at"])
	}
	if got := h.ok("seen", id); got["seen_at"] == nil {
		t.Error("seen didn't record seen_at")
	}

	// The panel's draft round trip: posted as the person, even with $CONDOO_AGENT set.
	f := h.ok("comment-file", id)
	path := f["path"].(string)
	draft, _ := os.ReadFile(path)
	for _, want := range []string{CommentCut, "# Comment on #" + id + ": Pick one?", "# Claude (lead):", "#   From the agent"} {
		if !strings.Contains(string(draft), want) {
			t.Errorf("draft lacks %q:\n%s", want, draft)
		}
	}
	if got := h.ok("comment-save", id, path); got["cancelled"] != true {
		t.Errorf("empty draft: %v", got)
	}
	h.ok("comment-file", id)
	os.WriteFile(path, []byte("Go with B.\r\n\r\n"+CommentCut+"\r\n# Comment on\r\n"), 0o600)
	if got := h.ok("comment-save", id, path); got["body"] != "Go with B." || got["author"] != "ada" || got["by_agent"] != false {
		t.Errorf("posted: %v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the draft must be removed")
	}
}
