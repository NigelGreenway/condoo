package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/NigelGreenway/condoo/internal/config"
	"github.com/NigelGreenway/condoo/internal/store"
)

// claimStale: a claim older than this is from a deliverer that died. Longer than hcTimeout,
// so a live one still waiting on `hc send` keeps its claim.
const (
	hcTimeout  = 100 * time.Second // `hc send` waits while the person types a draft (draft_wait)
	claimStale = 3 * time.Minute
)

// RunHC runs `hc` with args: its output and exit code, or an error if it couldn't run. hc
// prints its JSON errors on stderr, so a failure returns that instead of stdout.
func RunHC(args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hcTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hc", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if len(bytes.TrimSpace(exit.Stderr)) > 0 {
			out = exit.Stderr
		}
		return out, exit.ExitCode(), nil
	}
	if err != nil {
		return out, -1, err
	}
	return out, 0, nil
}

// HerdrDo runs a Herdr app command with JSON args and returns what it printed.
func HerdrDo(command string, args any) ([]byte, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "herdr-app", "do", command, string(b)).Output()
	if err != nil {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(out, &e) == nil && e.Error != "" {
			return out, errors.New(e.Error)
		}
		return out, fmt.Errorf("herdr-app do %s: %w (is Herdr running?)", command, err)
	}
	return out, nil
}

// The messages typed into agents.

func answerMessage(t *store.Task, answer string) string {
	return fmt.Sprintf("Answer to your question (condoo #%d) %q:\n\n%s", t.ID, t.Title, answer)
}

func commentMessage(t *store.Task, author, body string) string {
	return fmt.Sprintf("Comment from %s on condoo #%d %q:\n\n%s", author, t.ID, t.Title, body)
}

// assignMessage is what an agent is told when it's given an item. The agent that raised it
// (origin) is reminded it has the context; others are pointed at the item's comments.
// Agents only answer in comments: the person changes the status and closes it.
func assignMessage(t *store.Task, origin bool) string {
	var b strings.Builder
	what := map[string]string{"task": "task", "question": "question", "request": "request"}[t.Kind]
	fmt.Fprintf(&b, "Condoo #%d is assigned to you (a %s): %q\n", t.ID, what, t.Title)
	if t.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", t.Summary)
	}
	if t.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", t.Description)
	}
	if origin {
		b.WriteString("\nYou raised this item, so you have its context. Do it yourself, or if it's big, start a" +
			" sub-agent with a brief that carries what it needs from this conversation (a sub-agent" +
			" doesn't see it).\n")
	}
	fmt.Fprintf(&b, "\nWork on it now. `condoo get %d` shows it with its comments.\n", t.ID)
	fmt.Fprintf(&b, "Reply only with comments: `condoo comment %d \"...\" --agent \"<your name>\"`, for progress,"+
		" questions and the result. Don't change its status or close it: the person does that.\n", t.ID)
	b.WriteString("Comments the person adds to it are typed in here.")
	return b.String()
}

// combine joins several messages for one agent into one, so it isn't sent one per turn.
func combine(ds []store.Delivery) string {
	if len(ds) == 1 {
		return ds[0].Body
	}
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.Body
	}
	return fmt.Sprintf("%d updates from Condoo:\n\n%s", len(ds), strings.Join(parts, "\n\n---\n\n"))
}

// sendResult is how one agent's batch went.
type sendResult struct {
	Terminal string  `json:"terminal"`
	IDs      []int64 `json:"ids"`
	Result   string  `json:"result"` // sent | busy | gone
	Error    string  `json:"error,omitempty"`
}

type deliverResult struct {
	Delivered int          `json:"delivered"`
	Pending   int          `json:"pending"`
	Dead      int          `json:"dead"`
	Sends     []sendResult `json:"sends"`
}

// deliver types each agent's pending messages into its terminal (all of them, or one
// terminal's), combined into one message per agent, if it's idle or done. A busy agent's stay
// pending for the next try; a gone terminal's are marked dead.
func deliver(st *store.Store, terminal string, d Deps) (deliverResult, error) {
	res := deliverResult{Sends: []sendResult{}}
	claimed, err := st.Claim(terminal, claimStale)
	if err != nil {
		return res, err
	}
	var order []string
	byTerm := map[string][]store.Delivery{}
	for _, c := range claimed {
		if _, ok := byTerm[c.Terminal]; !ok {
			order = append(order, c.Terminal)
		}
		byTerm[c.Terminal] = append(byTerm[c.Terminal], c)
	}
	var firstErr error
	for _, term := range order {
		ds := byTerm[term]
		ids := make([]int64, len(ds))
		for i, x := range ds {
			ids[i] = x.ID
		}
		r := sendResult{Terminal: term, IDs: ids}
		state := "pending"
		ready, gone, why, err := agentReady(term, d)
		switch {
		case err != nil:
			r.Result, r.Error = "busy", err.Error()
			if firstErr == nil {
				firstErr = err
			}
		case gone:
			r.Result, r.Error, state = "gone", why, "dead"
		case !ready:
			r.Result, r.Error = "busy", why
		default:
			// Without --if: hc still waits out a draft the person is typing there and refuses a
			// blocked agent. If it started working since agentReady, the agent queues the text.
			out, code, err := d.HC("send", "--json", term, combine(ds))
			switch {
			case err != nil:
				r.Result, r.Error = "busy", "can't run hc: "+err.Error()
			case code == 0:
				r.Result, state = "sent", "delivered"
			default:
				kind, msg := hcError(out)
				if kind == "not_found" {
					r.Result, state = "gone", "dead"
				} else {
					r.Result = "busy"
				}
				r.Error = msg
			}
		}
		if err := st.Settle(ids, state, r.Error); err != nil {
			return res, err
		}
		switch state {
		case "delivered":
			res.Delivered += len(ids)
		case "dead":
			res.Dead += len(ids)
		default:
			res.Pending += len(ids)
		}
		res.Sends = append(res.Sends, r)
	}
	return res, firstErr
}

// agentReady asks hc about an agent's terminal: ready for a message (its agent is idle, or
// done: finished while nobody looked), gone (ended, or unknown), or neither and why.
func agentReady(term string, d Deps) (ready, gone bool, why string, err error) {
	out, code, err := d.HC("get", "--json", term)
	if err != nil {
		return false, false, "", fmt.Errorf("can't run hc: %w", err)
	}
	if code != 0 {
		kind, msg := hcError(out)
		if kind == "not_found" {
			return false, true, msg, nil
		}
		return false, false, msg, nil
	}
	var t struct {
		State  string `json:"state"`
		Status *struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &t); err != nil {
		return false, false, "can't read hc get: " + err.Error(), nil
	}
	switch {
	case t.State != "live":
		return false, true, "its program has ended", nil
	case t.Status == nil:
		return false, false, "no agent is running there", nil
	case t.Status.State == "idle" || t.Status.State == "done":
		return true, false, "", nil
	default:
		return false, false, "it's " + t.Status.State, nil
	}
}

// hcError reads hc's JSON error: its kind and message.
func hcError(out []byte) (string, string) {
	var e struct {
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &e) != nil || e.Error.Message == "" {
		return "", strings.TrimSpace(string(out))
	}
	return e.Error.Kind, e.Error.Message
}

// notify queues a message for an agent and, in instant mode, tries to type it in now.
// It returns "sent", "queued" (the agent is busy, or delivery isn't instant) or "gone".
func notify(st *store.Store, task *store.Task, terminal, kind, body string, cfg config.Resolved, d Deps) (string, error) {
	if _, err := st.Enqueue(task.ID, terminal, kind, body); err != nil {
		return "", err
	}
	if cfg.Delivery.Mode != "instant" || d.HC == nil {
		return "queued", nil
	}
	res, err := deliver(st, terminal, d)
	if err != nil {
		return "queued", nil // kept in the outbox; the panel tries again
	}
	for _, s := range res.Sends {
		switch s.Result {
		case "sent", "gone":
			return s.Result, nil
		}
	}
	return "queued", nil
}

// withDelivery adds how a message got on (or didn't) to a command's JSON output.
func withDelivery(v any, state string) (any, error) {
	if state == "" {
		return v, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["delivery"] = state
	return m, nil
}

// recipient: who gets the person's comments on an item: its assignee, else whoever raised it.
func recipient(t *store.Task) string {
	if t.Assignee != nil && *t.Assignee != "" {
		return *t.Assignee
	}
	if t.Terminal != nil {
		return *t.Terminal
	}
	return ""
}

// assign gives an item to an agent: a running one's terminal, "origin" for the agent that
// raised it (it has the context), or "new" to start one in a new tab in the item's space
// with the item as its first message.
func assign(st *store.Store, t *store.Task, to string, opts map[string]string, cfg config.Resolved, d Deps) (any, error) {
	if err := st.DiscardDead(t.ID); err != nil {
		return nil, err
	}
	if to == "origin" {
		if t.Terminal == nil || *t.Terminal == "" {
			return nil, bad("#%d wasn't raised by an agent: assign it to a terminal, or new", t.ID)
		}
		to = *t.Terminal
	}
	if to != "new" {
		if _, err := st.Update(t.ID, map[string]any{"assignee": to}); err != nil {
			return nil, err
		}
		state, err := notify(st, t, to, "assign", assignMessage(t, t.Terminal != nil && *t.Terminal == to), cfg, d)
		if err != nil {
			return nil, err
		}
		got, err := st.Get(t.ID)
		if err != nil {
			return nil, err
		}
		return withDelivery(got, state)
	}
	argv := splitCommand(opts["run"])
	if len(argv) == 0 {
		argv = []string{"claude"}
	}
	args := map[string]any{"name": fmt.Sprintf("condoo #%d", t.ID), "argv": argv, "task": assignMessage(t, false), "focus": false}
	if t.Space != nil && *t.Space != "" {
		args["space"] = *t.Space
	}
	if d.HerdrDo == nil {
		return nil, errors.New("can't start an agent without Herdr: start one and assign it by terminal")
	}
	out, err := d.HerdrDo("tab.new", args)
	if err != nil {
		return nil, fmt.Errorf("start an agent: %w", err)
	}
	var made struct {
		Terminal string `json:"terminal"`
	}
	if json.Unmarshal(out, &made) != nil || made.Terminal == "" {
		return nil, fmt.Errorf("start an agent: Herdr didn't say which terminal it made: %s", strings.TrimSpace(string(out)))
	}
	got, err := st.Update(t.ID, map[string]any{"assignee": made.Terminal})
	if err != nil {
		return nil, err
	}
	return withDelivery(got, "started")
}
