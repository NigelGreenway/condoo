// Package cli is the `condoo` command: agents ask and request through it, and the Herdr
// panel reads and changes items through it. Every command prints one JSON value.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/NigelGreenway/condoo/internal/config"
	"github.com/NigelGreenway/condoo/internal/store"
)

// Cut separates the answer from the question shown under it in the answer draft.
const Cut = "# ---- write your answer above this line; save and quit to send it, an empty answer cancels ----"

// CommentCut does the same for a comment draft.
const CommentCut = "# ---- write your comment above this line; save and quit to post it, an empty comment cancels ----"

const usage = `condoo: questions and requests from agents to you, and your own tasks.
Shown in Herdr's Condoo panel; stored in SQLite. Every command prints JSON.

Agents:
  condoo ask "What is required to do for this?" -s "one-line summary" -d "full context" \
      --agent "Claude (TICKET-123)" --priority normal
  condoo request "Approve the Linear reads" -s "..." -d "..." --agent "Codex (t2r)"
  condoo get 12                      its status and answer
  condoo set 12 --status completed
  condoo comment 12 "Done for menu-service" --agent "Claude (TICKET-123)"

Commands:
  ask TITLE [options]        a question to answer in writing (kind question)
  request TITLE [options]    something to act on or reply to (kind request)
  add TITLE [options]        one of your own tasks (kind task)
      -s, --summary TEXT       one short line, shown on the card
      -d, --description TEXT   the full context, options, a recommendation (markdown)
      --priority urgent|high|normal|low
      --status discussion|in_progress|up_next|completed|cancelled
      --agent NAME             who is asking (default $CONDOO_AGENT, else "agent")
      --terminal ADDR          where answers are typed (default $HC_TERMINAL_ID)
      --space ID               Herdr space (default: the terminal's; "" for none)
      --space-name NAME        its name (default: looked up in Herdr)
      --parent ID              make it a sub-item
      --waiting-on NAME        default: your name
  list [--kind request|task|question]
  get ID
  set ID [--title --summary --description --status --priority --waiting-on --agent --terminal]
  move ID --space ID [--space-name NAME]
                             the item and its sub-items to a Herdr space ("" for none)
  delete ID                  the item and its sub-items
  comment ID TEXT [--agent NAME]
                             a note on an item: by NAME (default $CONDOO_AGENT), else by you
  comments ID                an item's comments, oldest first (get includes them too)
  config                     your name, the database, and where each came from
  config set name|db VALUE   save to config.json ("" goes back to the default)
  version

Used by the panel: set-json ID JSON, answer-file ID, answer-save ID PATH, comment-file ID,
comment-save ID PATH, seen ID.

Environment: CONDOO_NAME, CONDOO_DB, CONDOO_CONFIG override config.json; CONDOO_AGENT,
HC_TERMINAL_ID fill --agent and --terminal.
`

// Deps is everything the CLI touches outside the database, so tests can replace it.
type Deps struct {
	Sys      config.System
	Login    func() string
	HerdrLS  func() []byte // `herdr-app ls` JSON, or nil when Herdr isn't running
	LookPath func(string) (string, error)
	Now      func() time.Time
	Stdout   io.Writer
	Version  string
}

// Default is the real machine.
func Default(version string) Deps {
	home, _ := os.UserHomeDir()
	return Deps{
		Sys:      config.System{GOOS: runtime.GOOS, Home: home, Env: os.Getenv},
		Login:    func() string { return config.LoginName(os.Getenv) },
		HerdrLS:  HerdrLS,
		LookPath: exec.LookPath,
		Now:      time.Now,
		Stdout:   os.Stdout,
		Version:  version,
	}
}

// usageError: a mistake in the command line (exit 2).
type usageError struct{ error }

// Run runs one command and returns the exit code.
func Run(args []string, d Deps) int {
	out, err := run(args, d)
	if err != nil {
		writeJSON(d.Stdout, map[string]string{"error": err.Error()})
		var ue usageError
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	if s, ok := out.(string); ok {
		fmt.Fprint(d.Stdout, s)
		return 0
	}
	writeJSON(d.Stdout, out)
	return 0
}

func writeJSON(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintln(w, string(b))
}

func bad(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

var itemFlags = []flag{
	{key: "summary", names: []string{"-s", "--summary"}},
	{key: "description", names: []string{"-d", "--description"}},
	{key: "status", names: []string{"--status"}, choices: store.Statuses},
	{key: "priority", names: []string{"--priority"}, choices: store.Priorities},
	{key: "waiting_on", names: []string{"--waiting-on"}},
	{key: "agent", names: []string{"--agent"}},
	{key: "terminal", names: []string{"--terminal"}},
	{key: "parent", names: []string{"--parent"}},
	{key: "space", names: []string{"--space"}},
	{key: "space_name", names: []string{"--space-name"}},
	{key: "kind", names: []string{"--kind"}, choices: store.Kinds},
}

var setFlags = []flag{
	{key: "title", names: []string{"--title"}},
	{key: "summary", names: []string{"-s", "--summary"}},
	{key: "description", names: []string{"-d", "--description"}},
	{key: "status", names: []string{"--status"}, choices: store.Statuses},
	{key: "priority", names: []string{"--priority"}, choices: store.Priorities},
	{key: "waiting_on", names: []string{"--waiting-on"}},
	{key: "agent", names: []string{"--agent"}},
	{key: "terminal", names: []string{"--terminal"}},
}

var moveFlags = []flag{
	{key: "space", names: []string{"--space"}},
	{key: "space_name", names: []string{"--space-name"}},
}

func run(args []string, d Deps) (any, error) {
	if len(args) == 0 {
		return usage, nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		return usage, nil
	case "version", "--version":
		return map[string]string{"version": d.Version}, nil
	case "config":
		return configCmd(rest, d)
	}

	cfg, err := config.Resolve(d.Sys, d.Login)
	if err != nil {
		return nil, err
	}
	var (
		flags []flag
		npos  int
	)
	switch cmd {
	case "list":
		flags = []flag{{key: "kind", names: []string{"--kind"}, choices: store.Kinds}}
	case "ask", "request", "add":
		flags, npos = itemFlags, 1
	case "set":
		flags, npos = setFlags, 1
	case "move":
		flags, npos = moveFlags, 1
	case "comment":
		flags, npos = []flag{{key: "agent", names: []string{"--agent"}}}, 2
	case "set-json", "answer-save", "comment-save":
		npos = 2
	case "get", "delete", "answer-file", "comments", "comment-file", "seen":
		npos = 1
	default:
		return nil, bad("unknown command %q (condoo --help)", cmd)
	}
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			return usage, nil
		}
	}
	pos, opts, err := parse(rest, flags)
	if err != nil {
		return nil, usageError{err}
	}
	if len(pos) != npos {
		return nil, bad("%s takes %d argument(s), got %d (condoo --help)", cmd, npos, len(pos))
	}

	st, err := store.Open(cfg.DB)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if d.Now != nil {
		st.Now = d.Now
	}

	switch cmd {
	case "list":
		tasks, err := st.List(opts["kind"])
		if err != nil {
			return nil, err
		}
		return map[string]any{"db": cfg.DB, "name": cfg.Name, "now": float64(st.Now().UnixNano()) / 1e9, "tasks": tasks}, nil
	case "ask", "request", "add":
		return add(st, cmd, pos[0], opts, cfg, d)
	}

	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil {
		return nil, bad("not an item id: %q", pos[0])
	}
	switch cmd {
	case "get":
		return st.Get(id)
	case "delete":
		if _, err := st.Get(id); err != nil {
			return nil, err
		}
		if err := st.Delete(id); err != nil {
			return nil, err
		}
		return map[string]int64{"deleted": id}, nil
	case "set":
		changes := map[string]any{}
		for k, v := range opts {
			changes[k] = v
		}
		return st.Update(id, changes)
	case "move":
		sp, ok := opts["space"]
		if !ok {
			return nil, bad("move needs --space (\"\" for none)")
		}
		space, name := spaceAndName(sp, opts, d)
		return st.Move(id, space, name)
	case "set-json":
		changes, err := decodeChanges(pos[1])
		if err != nil {
			return nil, err
		}
		return st.Update(id, changes)
	case "answer-file":
		return answerFile(st, id, d)
	case "answer-save":
		return answerSave(st, id, pos[1])
	case "comment":
		author, byAgent := commentAuthor(opts, cfg, d)
		return st.AddComment(id, author, byAgent, pos[1])
	case "comments":
		t, err := st.Get(id)
		if err != nil {
			return nil, err
		}
		return t.Comments, nil
	case "comment-file":
		return commentFile(st, id, d)
	case "comment-save":
		return commentSave(st, id, pos[1], cfg)
	case "seen":
		return st.Seen(id)
	}
	return nil, bad("unknown command %q", cmd)
}

func add(st *store.Store, cmd, title string, opts map[string]string, cfg config.Resolved, d Deps) (*store.Task, error) {
	kind := map[string]string{"ask": "question", "request": "request", "add": "task"}[cmd]
	if k, ok := opts["kind"]; ok {
		kind = k
	}
	n := store.NewTask{
		Kind: kind, Title: title, Summary: opts["summary"], Description: opts["description"],
		Status: opts["status"], Priority: opts["priority"], WaitingOn: cfg.Name,
	}
	if w, ok := opts["waiting_on"]; ok && w != "" {
		n.WaitingOn = w
	}
	agent, ok := opts["agent"]
	if !ok {
		agent = d.Sys.Env("CONDOO_AGENT")
	}
	if agent == "" && cmd != "add" {
		agent = "agent"
	}
	if ok || agent != "" {
		n.Agent = &agent
	}
	term, ok := opts["terminal"]
	if !ok {
		term = d.Sys.Env("HC_TERMINAL_ID")
	}
	if ok || term != "" {
		n.Terminal = &term
	}
	if p, ok := opts["parent"]; ok {
		pid, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, bad("--parent must be an item id")
		}
		n.ParentID = &pid
	}
	if sp, ok := opts["space"]; ok {
		n.Space, n.SpaceName = spaceAndName(sp, opts, d)
	} else if n.ParentID == nil && term != "" {
		if id, name := SpaceIn(herdrLS(d), term); id != "" {
			n.Space, n.SpaceName = &id, nonEmpty(name)
		}
	}
	return st.Add(n)
}

// spaceAndName: the space from --space, with its name from --space-name or, failing that,
// from Herdr. "" means no space.
func spaceAndName(sp string, opts map[string]string, d Deps) (*string, *string) {
	if sp == "" {
		return nil, nil
	}
	name, ok := opts["space_name"]
	if !ok {
		name = SpaceName(herdrLS(d), sp)
	}
	return &sp, nonEmpty(name)
}

func herdrLS(d Deps) []byte {
	if d.HerdrLS == nil {
		return nil
	}
	return d.HerdrLS()
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// decodeChanges reads set-json's object; whole numbers become integers (parent_id).
func decodeChanges(s string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, bad("set-json needs a JSON object: %v", err)
	}
	for k, v := range m {
		if f, ok := v.(float64); ok {
			if f != math.Trunc(f) {
				return nil, bad("%s must be a whole number", k)
			}
			m[k] = int64(f)
		}
	}
	return m, nil
}

func configCmd(args []string, d Deps) (any, error) {
	if len(args) == 0 {
		return config.Resolve(d.Sys, d.Login)
	}
	if args[0] != "set" || len(args) != 3 {
		return nil, bad("usage: condoo config [set name|db VALUE]")
	}
	path := config.ConfigFile(d.Sys)
	f, err := config.Read(path)
	if err != nil {
		return nil, err
	}
	switch args[1] {
	case "name":
		f.Name = strings.TrimSpace(args[2])
	case "db":
		f.DB = strings.TrimSpace(args[2])
	default:
		return nil, bad("config keys: name, db")
	}
	if err := config.Write(path, f); err != nil {
		return nil, err
	}
	return config.Resolve(d.Sys, d.Login)
}

// answerFile writes a draft to answer a question in: the current answer, then the question
// as comments below the cut line. It also says which editor to open it with.
func answerFile(st *store.Store, id int64, d Deps) (any, error) {
	t, err := st.Get(id)
	if err != nil {
		return nil, err
	}
	dir := config.AnswerDir(d.Sys)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("answer-%d.md", id))
	lines := []string{Cut, "#", "# Question: " + t.Title}
	if t.Summary != "" {
		lines = append(lines, "# "+t.Summary, "#")
	}
	if t.Description != "" {
		for _, l := range strings.Split(t.Description, "\n") {
			lines = append(lines, "# "+l)
		}
	}
	if t.Agent != nil && *t.Agent != "" {
		lines = append(lines, "# Asked by: "+*t.Agent)
	}
	cur := ""
	if t.Answer != nil {
		cur = strings.TrimRight(*t.Answer, " \t\r\n")
	}
	body := cur + "\n\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "editor": Editor(d)}, nil
}

// Editor is the command to edit an answer with: $VISUAL, $EDITOR, else the first terminal
// editor found (Windows' notepad last: it opens outside the terminal).
func Editor(d Deps) []string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if w := splitCommand(d.Sys.Env(k)); len(w) > 0 {
			return w
		}
	}
	candidates := []string{"vim", "vi", "nano"}
	if d.Sys.GOOS == "windows" {
		candidates = []string{"edit", "nvim", "vim", "nano", "notepad"}
	}
	for _, c := range candidates {
		if d.LookPath == nil {
			break
		}
		if _, err := d.LookPath(c); err == nil {
			return []string{c}
		}
	}
	return []string{candidates[0]}
}

func answerSave(st *store.Store, id int64, path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	os.Remove(path)
	text, _, _ := strings.Cut(strings.ReplaceAll(string(b), "\r\n", "\n"), Cut)
	text = strings.TrimSpace(text)
	t, err := st.Get(id)
	if err != nil {
		return nil, err
	}
	old := ""
	if t.Answer != nil {
		old = strings.TrimSpace(*t.Answer)
	}
	if text == "" || text == old {
		return map[string]bool{"cancelled": true}, nil
	}
	waiting := "agent"
	if t.Agent != nil && *t.Agent != "" {
		waiting = *t.Agent
	}
	return st.Update(id, map[string]any{"answer": text, "status": "completed", "waiting_on": waiting})
}

// HerdrLS asks the open Herdr window for its spaces (nil if it can't).
func HerdrLS() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "herdr-app", "ls").Output()
	if err != nil {
		return nil
	}
	return out
}

type lsSpace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tabs []struct {
		Terminals []json.RawMessage `json:"terminals"`
	} `json:"tabs"`
}

func lsSpaces(lsJSON []byte) []lsSpace {
	var doc struct {
		Spaces []lsSpace `json:"spaces"`
	}
	if json.Unmarshal(lsJSON, &doc) != nil {
		return nil
	}
	return doc.Spaces
}

// SpaceName is a space's name in `herdr-app ls` JSON ("" if it isn't open).
func SpaceName(lsJSON []byte, id string) string {
	for _, sp := range lsSpaces(lsJSON) {
		if sp.ID == id {
			return sp.Name
		}
	}
	return ""
}

// SpaceIn finds the space a terminal (or sub-agent) is in, in `herdr-app ls` JSON: its id and
// name, or "" and "".
func SpaceIn(lsJSON []byte, terminal string) (string, string) {
	if terminal == "" {
		return "", ""
	}
	type term struct {
		Terminal  string            `json:"terminal"`
		SubAgents []json.RawMessage `json:"sub_agents"`
	}
	var has func([]json.RawMessage) bool
	has = func(list []json.RawMessage) bool {
		for _, raw := range list {
			var t term
			if json.Unmarshal(raw, &t) != nil {
				continue
			}
			if t.Terminal == terminal || has(t.SubAgents) {
				return true
			}
		}
		return false
	}
	for _, sp := range lsSpaces(lsJSON) {
		for _, tab := range sp.Tabs {
			if has(tab.Terminals) {
				return sp.ID, sp.Name
			}
		}
	}
	return "", ""
}

// commentAuthor: an agent's name from --agent or $CONDOO_AGENT, else the person's own name.
func commentAuthor(opts map[string]string, cfg config.Resolved, d Deps) (string, bool) {
	agent, ok := opts["agent"]
	if !ok {
		agent = d.Sys.Env("CONDOO_AGENT")
	}
	if agent = strings.TrimSpace(agent); agent != "" {
		return agent, true
	}
	return cfg.Name, false
}

// commentFile writes a draft to comment in: an empty space, then the item and its last few
// comments as comment lines below the cut. It also says which editor to open it with.
func commentFile(st *store.Store, id int64, d Deps) (any, error) {
	t, err := st.Get(id)
	if err != nil {
		return nil, err
	}
	dir := config.AnswerDir(d.Sys)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("comment-%d.md", id))
	lines := []string{CommentCut, "#", fmt.Sprintf("# Comment on #%d: %s", t.ID, t.Title)}
	if t.Summary != "" {
		lines = append(lines, "# "+t.Summary)
	}
	recent := t.Comments
	if len(recent) > 5 {
		recent = recent[len(recent)-5:]
	}
	for _, c := range recent {
		lines = append(lines, "#", "# "+c.Author+":")
		for _, l := range strings.Split(c.Body, "\n") {
			lines = append(lines, "#   "+l)
		}
	}
	if err := os.WriteFile(path, []byte("\n\n"+strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "editor": Editor(d)}, nil
}

// commentSave posts the draft as the person's comment and removes it.
func commentSave(st *store.Store, id int64, path string, cfg config.Resolved) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	os.Remove(path)
	text, _, _ := strings.Cut(strings.ReplaceAll(string(b), "\r\n", "\n"), CommentCut)
	if text = strings.TrimSpace(text); text == "" {
		return map[string]bool{"cancelled": true}, nil
	}
	return st.AddComment(id, cfg.Name, false, text)
}
