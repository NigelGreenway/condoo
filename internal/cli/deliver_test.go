package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeHC stands in for `hc get` and `hc send`: each terminal is idle, done, working,
// blocked, shell (no agent), finished or gone (unknown).
type fakeHC struct {
	status map[string]string
	sent   map[string][]string // what each terminal was sent, in order
}

func (h *harness) withHC() *fakeHC {
	f := &fakeHC{status: map[string]string{}, sent: map[string][]string{}}
	notFound := func(term string) ([]byte, int, error) {
		return []byte(`{"error":{"kind":"not_found","message":"no terminal \"` + term + `\""}}`), 3, nil
	}
	h.deps.HC = func(args ...string) ([]byte, int, error) {
		switch {
		case len(args) == 3 && args[0] == "get" && args[1] == "--json":
			switch st := f.status[args[2]]; st {
			case "", "gone":
				return notFound(args[2])
			case "finished":
				return []byte(`{"state":"finished","status":null}`), 0, nil
			case "shell":
				return []byte(`{"state":"live","status":null}`), 0, nil
			default:
				return []byte(`{"state":"live","status":{"app":"claude-code","state":"` + st + `"}}`), 0, nil
			}
		case len(args) == 4 && args[0] == "send" && args[1] == "--json":
			term := args[2]
			if st := f.status[term]; st == "" || st == "gone" {
				return notFound(term)
			}
			f.sent[term] = append(f.sent[term], args[3])
			return []byte(`{"sent":true}`), 0, nil
		}
		return nil, -1, errors.New("unexpected hc call: " + strings.Join(args, " "))
	}
	return f
}

func outbox(t *testing.T, task map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	b, _ := json.Marshal(task["outbox"])
	json.Unmarshal(b, &out)
	return out
}

func TestCommentReachesIdleAgentAtOnce(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	hc.status["t7"] = "idle"
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude", "--terminal", "t7"))

	got := h.ok("comment", id, "Go with B", "--agent", "")
	if got["delivery"] != "sent" {
		t.Errorf("delivery: %v", got)
	}
	if len(hc.sent["t7"]) != 1 || !strings.Contains(hc.sent["t7"][0], `Comment from ada on condoo #`+id+` "Pick one?":`) ||
		!strings.HasSuffix(hc.sent["t7"][0], "Go with B") {
		t.Errorf("sent: %q", hc.sent["t7"])
	}
	if ob := outbox(t, h.ok("get", id)); len(ob) != 0 {
		t.Errorf("delivered message still in the outbox: %v", ob)
	}
}

func TestAgentsOwnCommentIsNotSentBack(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	hc.status["t7"] = "idle"
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude", "--terminal", "t7"))
	if got := h.ok("comment", id, "progress", "--agent", "Claude"); got["delivery"] != nil {
		t.Errorf("agent comment delivered: %v", got)
	}
	if len(hc.sent) != 0 {
		t.Errorf("sent: %v", hc.sent)
	}
}

func TestBusyAgentGetsEverythingOnceWhenIdle(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	hc.status["t7"] = "working"
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude", "--terminal", "t7"))

	if got := h.ok("comment", id, "first", "--agent", ""); got["delivery"] != "queued" {
		t.Errorf("busy: %v", got)
	}
	path := h.ok("answer-file", id)["path"].(string)
	os.WriteFile(path, []byte("Option B\n\n"+Cut+"\n"), 0o600)
	if got := h.ok("answer-save", id, path); got["delivery"] != "queued" || got["status"] != "completed" {
		t.Errorf("answer while busy: %v", got)
	}
	ob := outbox(t, h.ok("get", id))
	if len(ob) != 2 || ob[0]["state"] != "pending" || ob[1]["kind"] != "answer" {
		t.Errorf("outbox while busy: %v", ob)
	}

	// Still busy: nothing sent, nothing lost.
	if got := h.ok("deliver"); got["pending"] != 2.0 || got["delivered"] != 0.0 {
		t.Errorf("deliver while busy: %v", got)
	}

	hc.status["t7"] = "done" // finished while nobody looked: ready too
	if got := h.ok("deliver"); got["delivered"] != 2.0 {
		t.Errorf("deliver when idle: %v", got)
	}
	if len(hc.sent["t7"]) != 1 {
		t.Fatalf("want one combined message, got %q", hc.sent["t7"])
	}
	msg := hc.sent["t7"][0]
	if !strings.HasPrefix(msg, "2 updates from Condoo:") || !strings.Contains(msg, "first") ||
		!strings.Contains(msg, "Answer to your question (condoo #"+id+")") || strings.Index(msg, "first") > strings.Index(msg, "Option B") {
		t.Errorf("combined message: %q", msg)
	}

	// Nothing twice.
	if got := h.ok("deliver"); got["delivered"] != 0.0 || len(hc.sent["t7"]) != 1 {
		t.Errorf("delivered again: %v %q", got, hc.sent["t7"])
	}
	if ob := outbox(t, h.ok("get", id)); len(ob) != 0 {
		t.Errorf("outbox after delivery: %v", ob)
	}
}

func TestGoneAgentIsMarkedDead(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude", "--terminal", "t9"))
	if got := h.ok("comment", id, "hello?", "--agent", ""); got["delivery"] != "gone" {
		t.Errorf("gone: %v", got)
	}
	ob := outbox(t, h.ok("get", id))
	if len(ob) != 1 || ob[0]["state"] != "dead" {
		t.Errorf("outbox: %v", ob)
	}
	// Dead ones aren't retried.
	if got := h.ok("deliver"); got["dead"] != 0.0 || len(got["sends"].([]any)) != 0 {
		t.Errorf("retried a dead one: %v", got)
	}

	// Assigning it to someone else drops them; that agent gets the item, and later comments.
	hc.status["t3"] = "idle"
	got := h.ok("assign", id, "t3")
	if got["assignee"] != "t3" || got["delivery"] != "sent" || len(outbox(t, got)) != 0 {
		t.Errorf("assigned: %v", got)
	}
	if msg := hc.sent["t3"]; len(msg) != 1 || !strings.Contains(msg[0], "Condoo #"+id+" is assigned to you (a question)") ||
		!strings.Contains(msg[0], "Don't change its status") || strings.Contains(msg[0], "You raised this") ||
		strings.Contains(msg[0], "--status") {
		t.Errorf("assignment: %q", msg)
	}
	h.ok("comment", id, "thanks", "--agent", "")
	if len(hc.sent["t3"]) != 2 || !strings.HasSuffix(hc.sent["t3"][1], "thanks") {
		t.Errorf("comment after assigning: %q", hc.sent["t3"])
	}
}

func TestDeliveryModeOffOnlyQueues(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	hc.status["t7"] = "idle"
	h.env["CONDOO_DELIVERY_MODE"] = "off"
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude", "--terminal", "t7"))
	if got := h.ok("comment", id, "later", "--agent", ""); got["delivery"] != "queued" || len(hc.sent) != 0 {
		t.Errorf("off: %v %v", got, hc.sent)
	}
	// An explicit deliver still sends.
	if got := h.ok("deliver", "--terminal", "t7"); got["delivered"] != 1.0 {
		t.Errorf("deliver: %v", got)
	}
}

func TestAssignNewStartsAnAgentTab(t *testing.T) {
	h := newHarness(t)
	h.withHC()
	var gotCmd string
	var gotArgs map[string]any
	h.deps.HerdrDo = func(command string, args any) ([]byte, error) {
		gotCmd = command
		b, _ := json.Marshal(args)
		json.Unmarshal(b, &gotArgs)
		return []byte(`{"space":"s1","tab":"tb2","terminal":"t42"}`), nil
	}
	id := jsonID(h.ok("add", "Fix the login", "-d", "It 500s", "--space", "s1", "--space-name", "app"))
	got := h.ok("assign", id, "new", "--run", "codex --yolo")
	if got["assignee"] != "t42" || got["delivery"] != "started" {
		t.Errorf("assigned: %v", got)
	}
	if gotCmd != "tab.new" || gotArgs["space"] != "s1" || gotArgs["focus"] != false ||
		gotArgs["name"] != "condoo #"+id || !strings.Contains(gotArgs["task"].(string), "It 500s") {
		t.Errorf("tab.new %v", gotArgs)
	}
	if argv, _ := gotArgs["argv"].([]any); len(argv) != 2 || argv[0] != "codex" {
		t.Errorf("argv: %v", gotArgs["argv"])
	}
}

func TestConfigSetDelivery(t *testing.T) {
	h := newHarness(t)
	got := h.ok("config")
	if d := got["delivery"].(map[string]any); d["mode"] != "instant" || d["interval_seconds"] != 30.0 || d["mode_source"] != "default" {
		t.Errorf("defaults: %v", d)
	}
	h.ok("config", "set", "delivery.mode", "interval")
	got = h.ok("config", "set", "delivery.interval", "60")
	if d := got["delivery"].(map[string]any); d["mode"] != "interval" || d["interval_seconds"] != 60.0 || d["interval_source"] != "config" {
		t.Errorf("set: %v", d)
	}
	if code, _ := h.run("config", "set", "delivery.mode", "sometimes"); code != 2 {
		t.Errorf("bad mode: exit %d", code)
	}
	if code, _ := h.run("config", "set", "delivery.interval", "2"); code != 2 {
		t.Errorf("too short: exit %d", code)
	}
	h.ok("config", "set", "delivery.mode", "")
	h.ok("config", "set", "delivery.interval", "")
	if b, _ := os.ReadFile(h.env["CONDOO_CONFIG"]); strings.Contains(string(b), "delivery") {
		t.Errorf("an emptied delivery block is left in config.json: %s", b)
	}
}

func TestOnlyIdleOrDoneAgentsAreTyped(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	id := jsonID(h.ok("ask", "Pick one?", "--agent", "Claude", "--terminal", "t7"))
	for _, st := range []string{"working", "blocked", "shell"} {
		hc.status["t7"] = st
		if got := h.ok("comment", id, st, "--agent", ""); got["delivery"] != "queued" {
			t.Errorf("%s: %v", st, got)
		}
	}
	if len(hc.sent) != 0 {
		t.Errorf("typed into a busy terminal: %v", hc.sent)
	}
	hc.status["t7"] = "finished"
	if got := h.ok("deliver"); got["dead"] != 3.0 {
		t.Errorf("finished: %v", got)
	}
}

func TestAssignBackToTheAgentThatRaisedIt(t *testing.T) {
	h := newHarness(t)
	hc := h.withHC()
	hc.status["t7"] = "idle"
	id := jsonID(h.ok("request", "Look at the flaky test", "--agent", "Claude", "--terminal", "t7"))
	got := h.ok("assign", id, "origin")
	if got["assignee"] != "t7" || got["delivery"] != "sent" {
		t.Errorf("assigned: %v", got)
	}
	if len(hc.sent["t7"]) != 1 || !strings.Contains(hc.sent["t7"][0], "You raised this item") {
		t.Errorf("message: %q", hc.sent["t7"])
	}
	mine := jsonID(h.ok("add", "My own"))
	if code, _ := h.run("assign", mine, "origin"); code != 2 {
		t.Errorf("origin of a person's task: exit %d", code)
	}
}
