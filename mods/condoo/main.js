// Condoo (Context + Todo): a sidebar of (1) things agents need you to reply to, (2) your own
// tasks and (3) agents' questions, stored in SQLite through the `condoo` command. Vim-style
// keys (see key()).
//
// One mod, several roles:
//   - the panel: a block showing this mod in your layout (any id), or,
//     in a layout without one, a dialog (props.role "panel").
//   - the focus key: opened by `mod.open:condoo`. It closes itself at once, then shows the
//     panel (and any hidden column around it) and gives it the keyboard. It never hides it:
//     hide the column it sits in with your sidebar key instead.
//   - the answer bar (props.role "answer-bar"): the title over the editor terminal in the
//     answer dialog. Enter on a question opens $EDITOR there; on exit the answer is stored and
//     typed into the asking agent if it's idle.
//   - the help (props.role "help"): `?` opens a dialog summarising the sections and keys.

import { View, div, svg } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea, TextView, ScrollHandle } from "gpui-base";
import {
  props, theme, settings, exec, appHome, home, run, layout, focused, keys, readTextFile, machines,
  shared, setShared, closeOverlay, openOverlay, nextCommand, notify, hc, terminals, spaces, panes,
} from "herdr";
import { spaceIndex as spaceIndexOf, DETACHED } from "./spaces.js";

const MOD = "condoo";           // a layout block showing this mod holds the panel, whatever its id
const FOCUS = "condoo.focus";   // shared: a timestamp asking the panel to take the keyboard
const DIALOG = "condoo.dialog"; // shared: the panel dialog's overlay id (layouts without the block)
const RELOAD = "condoo.reload"; // shared: a timestamp asking every panel to reload (after a move)
const STATUSES = ["discussion", "in_progress", "up_next", "completed", "cancelled"];
const STATUS_LABEL = {
  discussion: "Waiting for discussion",
  in_progress: "In Progress",
  up_next: "Up Next",
  completed: "Completed",
  cancelled: "Cancelled",
};
const CLOSED = new Set(["completed", "cancelled"]);
const PRIORITIES = ["urgent", "high", "normal", "low"];
const PRIORITY_LABEL = { urgent: "Urgent", high: "High", normal: "Normal", low: "Low" };
const SECTIONS = [
  { kind: "request", label: "Needs your reply", empty: "Nothing waiting on you" },
  { kind: "task", label: "My tasks", empty: "No tasks. Press a to add one" },
  { kind: "question", label: "Questions", empty: "No open questions" },
];
const FIELDS = ["title", "summary", "description", "waiting_on"];
const FIELD_LABEL = { title: "Title", summary: "Summary", description: "Description", waiting_on: "Waiting on" };
const TITLE_SIZE = 13;
const SHOWN_COMMENTS = 5; // the latest few; "show N earlier" for the rest
// f cycles: open (null: everything but completed and cancelled), each status, then all.
const FILTERS = [null, ...STATUSES, "all"];
const POLL_MS = 4000;
const DRAFT_LABEL = "condoo.draft"; // on an editor terminal: what it drafts, "answer:ID" | "comment:ID"
const PATH_LABEL = "condoo.path";   // on an editor terminal: the draft file

// The `?` dialog: [title, intro, [[keys, what], ...]]
const HELP = [
  ["The panel", "Three sections. Items belong to the space they were raised in, and follow it if it's closed and opened again with the same name. Items with no space sit under Personal tasks, and items whose space is closed under Detached Spaces; both show in every space.", [
    ["Needs your reply", "requests from agents: something to do or reply to"],
    ["My tasks", "your own tasks and sub-tasks"],
    ["Questions", "agents' questions to answer in writing"],
  ]],
  ["Moving", null, [
    ["j k  ↓ ↑", "next / previous item"],
    ["g G", "first / last item"],
    ["[ ]", "previous / next section (empty ones too)"],
    ["space", "show / hide the description and comments"],
    ["l", "fold / unfold this item's sub-tasks"],
  ]],
  ["Items", null, [
    ["enter", "question: answer it in $EDITOR (a draft still open opens again) · request: go to the agent · task: edit"],
    ["a o", "add an item to this section"],
    [">", "add a sub-task"],
    ["e", "edit"],
    ["c", "comment, in $EDITOR (typed into the agent too, if it's idle; a draft still open opens again)"],
    ["s S", "next / previous status"],
    ["p P", "next / previous priority"],
    ["m", "move to another space (with its sub-tasks)"],
    ["x", "delete (y to confirm)"],
  ]],
  ["View", null, [
    ["w", "this space / all spaces"],
    ["f", "filter: open → each status → all"],
    ["r", "reload"],
    ["esc  q", "back to the pane (panel stays open)"],
    ["{toggle}", "show the panel and focus it"],
    ["?", "this help"],
  ]],
  ["Editing", null, [
    ["tab  shift-tab", "next / previous field"],
    ["enter", "next field (a new line in Description)"],
    ["esc", "save and close the form"],
  ]],
];

// Which space an item belongs to (live data in, so spaces.js stays testable).
const spaceIndex = () => spaceIndexOf(spaces(), panes(), machines());

// Soft colours that stay readable on dark and light themes.
function palette(dark) {
  return dark
    ? { maroon: "#c4677a", yellow: "#e5c07b", green: "#98c379", red: "#e06c75", orange: "#d19a66" }
    : { maroon: "#800000", yellow: "#9a7212", green: "#3f7d2c", red: "#b8323a", orange: "#b5651d" };
}

// The layout block that shows this mod, found by its content rather than its id, so any
// block a person or agent adds (`{ "content": { "mod": "condoo" } }`) works. It needs an id
// for layout.show; without one the panel opens as a dialog instead. Returns the block and
// every node above it, outermost first: showing the block alone does nothing while the
// column it sits in is hidden.
function pathTo(n, path = []) {
  if (!n || typeof n !== "object") return null;
  const here = [...path, n];
  if (n.content?.mod === MOD && n.id) return here;
  for (const c of [...(n.children ?? []), ...(n.floating ?? [])]) {
    const r = pathTo(c, here);
    if (r) return r;
  }
  return null;
}

const panelShown = () => {
  const path = pathTo(layout());
  return Boolean(path && !path.some((n) => n.hidden));
};

// The key bound to the focus key, for the help.
const toggleKey = () => keys().find((k) => k.action === "mod.open:condoo")?.keys ?? "your focus key";

// The installer records where it put the binary in bin.json, so the panel works even when
// that folder isn't on the PATH Herdr sees. Without it: `condoo` from PATH.
async function condooPath() {
  try {
    const p = JSON.parse((await readTextFile(`${appHome()}/mods/condoo/bin.json`)) ?? "null")?.path;
    if (p) return p;
  } catch (_e) { /* fall back */ }
  return "condoo";
}

async function cli(args) {
  const bin = await condooPath();
  let r;
  try {
    r = await exec(bin, args, { timeout: 10000 });
  } catch (e) {
    throw new Error(`Can't run ${bin}: is Condoo installed? (${e.message ?? e})`);
  }
  let out = null;
  try { out = JSON.parse(r.stdout); } catch (_e) { /* below */ }
  if (r.code !== 0 || !out || out.error) {
    throw new Error(out?.error ?? (r.stderr.trim().split("\n").pop() || `condoo exited ${r.code}`));
  }
  return out;
}

function load(key, fallback) {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : JSON.parse(v);
  } catch (_e) { return fallback; }
}
function save(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch (_e) { /* best effort */ }
}

const next = (list, v, step = 1) => list[(list.indexOf(v) + step + list.length) % list.length];

// An agent commented since the person last opened the item (their own comments never count).
const unread = (task) => (task.comments ?? []).some((c) => c.by_agent && c.created_at > (task.seen_at ?? 0));

// "just now", "12m ago", "3h ago", "2d ago".
function ago(seconds) {
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export default class Condoo extends View {
  init(_props, cx) {
    if (props()?.role === "answer-bar") return;
    if (props()?.role === "help" || props()?.role === "move") {
      this.h = cx.focus_handle();
      this.pick = 0;
      cx.timer.after(30, () => this.h.focus());
      return;
    }
    if (props()?.overlay && props()?.role !== "panel") return this.focusPanel(cx);

    this.tasks = [];
    this.loaded = false;
    this.error = null;
    this.cursor = null;      // selected task id
    this.cursorIndex = 0;    // its place, to land nearby when it disappears
    this.mode = "nav";       // nav | edit | confirm
    this.edit = null;        // { id | null, parent_id, kind, field }
    this.filter = load("filter", null);       // null: open items, a status, or "all"
    if (!FILTERS.includes(this.filter)) this.filter = null;
    this.allSpaces = load("allSpaces", false);  // false: only the focused space's items
    this.folded = new Set(load("folded", []));   // l: task ids whose sub-tasks are folded away
    this.focusToken = null;
    this.expanded = null;    // task id whose description is open (space)
    this.allComments = null; // task id whose earlier comments are shown too
    this.hovered = null;     // task id under the pointer: sub-tasks show in full then
    this.section = "task";   // the section the cursor is in: `a` adds there
    this.onEmpty = false;    // the cursor sits on an empty section (reached with [ ])

    this.h = cx.focus_handle();
    this.scroll = ScrollHandle.new();
    this.starting = new Set(); // draft keys whose editor is being started
    this.waiting = new Set();  // editor addresses this copy saves when they exit
    this.dialogs = new Map();  // editor address → the dialog showing it
    this.adoptDrafts(cx);
    this.inputs = {};
    for (const f of FIELDS) {
      if (f === "description") {
        // Long text: Enter is a new line here; tab moves on, esc saves.
        this.inputs[f] = TextareaState.new({ placeholder: "Full context (markdown)", rows: 6 });
        this.inputs[f].set_auto_grow(6, 18);
      } else {
        this.inputs[f] = InputState.new({ placeholder: FIELD_LABEL[f] });
        this.inputs[f].on("submit", (_e, cx) => this.nextField(1, true, cx));
      }
      this.inputs[f].on("focus", (_e, cx) => { if (this.edit) { this.edit.field = f; cx.notify(); } });
    }

    this.reloadToken = shared(RELOAD) ?? null; // only later moves trigger a reload
    this.reload(cx);
    cx.timer.every(POLL_MS, (cx) => { if (this.mode === "nav" && this.visible()) this.reload(cx); });
    cx.spawn(async (cx) => {
      for (;;) {
        const c = await nextCommand();
        if (c === "focus") { this.h.focus(); this.reload(cx); }
        cx.notify();
      }
    });
  }

  visible() {
    return props()?.role === "panel" || panelShown();
  }

  // The focus key: show the panel (and any hidden node around it) and give it the keyboard.
  // It never hides anything. Layouts without a `condoo` block get the panel as a dialog instead.
  focusPanel(_cx) {
    closeOverlay(props().overlay);
    const path = pathTo(layout());
    if (!path) {
      if (!shared(DIALOG)) {
        setShared(DIALOG, openOverlay({ mod: "condoo", props: { role: "panel" }, kind: "dialog", width: 420, height: 0.85 }));
      }
      setShared(FOCUS, Date.now());
      return;
    }
    for (const n of path) if (n.hidden && n.id) run("layout.show", { id: n.id });
    setShared(FOCUS, Date.now());
  }

  // ---------- data ----------

  reload(cx) {
    cx.spawn(async (cx) => {
      try {
        const out = await cli(["list"]);
        this.tasks = out.tasks;
        this.userName = out.name;
        this.db = out.db;
        this.error = null;
        // Follow renamed spaces, and spaces closed and opened again with the same name.
        const idx = spaceIndex();
        for (const t of this.tasks) {
          const change = idx.fix(t);
          if (!change) continue;
          Object.assign(t, change);
          await cli(["set-json", String(t.id), JSON.stringify(change)]);
        }
      } catch (e) {
        this.error = String(e.message ?? e);
      }
      this.loaded = true;
      cx.notify();
    });
  }

  mutate(cx, args, after) {
    cx.spawn(async (cx) => {
      try {
        const out = await cli(args);
        if (after) after(out);
      } catch (e) {
        notify({ title: "Condoo", body: String(e.message ?? e), level: "error", timeout: 5000 });
      }
      this.reload(cx);
    });
  }

  setFields(cx, task, changes) {
    Object.assign(task, changes);
    if (changes.status) task.status_since = Date.now() / 1000;
    this.mutate(cx, ["set-json", String(task.id), JSON.stringify(changes)]);
    cx.notify();
  }

  // ---------- the list ----------

  here() {
    return focused()?.space ?? null;
  }

  // Top-level items in scope: this space's, plus those with no space. Sub-items follow their parent.
  inScope(t, idx = this.idx ?? spaceIndex()) {
    if (this.allSpaces) return true;
    const sp = idx.of(t);
    return !sp || sp === DETACHED || sp === this.here();
  }

  matches(t) {
    if (this.filter === "all") return true;
    if (this.filter) return t.status === this.filter;
    return !CLOSED.has(t.status);
  }

  // Flattened, ordered rows: section headers, then tasks depth-first by priority.
  rows() {
    this.idx = spaceIndex();
    const byParent = new Map();
    for (const t of this.tasks) {
      const k = t.parent_id ?? 0;
      if (!byParent.has(k)) byParent.set(k, []);
      byParent.get(k).push(t);
    }
    const order = (a, b) => PRIORITIES.indexOf(a.priority) - PRIORITIES.indexOf(b.priority) || a.id - b.id;
    const keep = new Map();
    const visible = (t) => {
      if (keep.has(t.id)) return keep.get(t.id);
      const kids = byParent.get(t.id) ?? [];
      const v = this.matches(t) || (!this.folded.has(t.id) && kids.some(visible));
      keep.set(t.id, v);
      return v;
    };
    const out = [];
    for (const s of SECTIONS) {
      out.push({ header: s });
      let any = false;
      const walk = (list, depth) => {
        for (const t of [...list].sort(order)) {
          if (!visible(t)) continue;
          any = true;
          out.push({ task: t, depth, context: !this.matches(t) });
          if (this.edit && this.edit.id === null && this.edit.parent_id === t.id) {
            out.push({ draft: true, depth: depth + 1 });
          }
          if (!this.folded.has(t.id)) walk(byParent.get(t.id) ?? [], depth + 1);
        }
      };
      if (this.edit && this.edit.id === null && this.edit.parent_id === null && this.edit.kind === s.kind) {
        out.push({ draft: true, depth: 0 });
        any = true;
      }
      // Space items first; then, under their own dividers, the ones shown in every space:
      // no space (Personal tasks), and a closed space (Detached Spaces).
      const roots = (byParent.get(0) ?? []).filter((t) => t.kind === s.kind && this.inScope(t));
      const personal = roots.filter((t) => this.idx.of(t) === null);
      const detached = roots.filter((t) => this.idx.of(t) === DETACHED);
      walk(roots.filter((t) => this.idx.of(t) && this.idx.of(t) !== DETACHED), 0);
      if (personal.some(visible)) out.push({ divider: `${s.kind}-personal`, label: "Personal tasks" });
      walk(personal, 0);
      if (detached.some(visible)) out.push({ divider: `${s.kind}-detached`, label: "Detached Spaces" });
      walk(detached, 0);
      if (!any) out.push({ empty: s });
    }
    return out;
  }

  selectable(rows) {
    return rows.filter((r) => r.task && !r.context).map((r) => r.task);
  }

  current(rows) {
    const items = this.selectable(rows);
    if (this.onEmpty) {
      // Something arrived in the empty section we're on: land on it.
      const first = items.find((t) => t.kind === this.section);
      if (!first) { this.cursor = null; return null; }
      this.onEmpty = false;
      this.cursor = first.id;
    }
    if (!items.length) { this.cursor = null; return null; }
    let i = items.findIndex((t) => t.id === this.cursor);
    if (i < 0) i = Math.min(this.cursorIndex, items.length - 1);
    this.cursor = items[i].id;
    this.cursorIndex = i;
    this.section = items[i].kind;
    return items[i];
  }

  move(step, cx) {
    const items = this.selectable(this.rows());
    if (!items.length) return;
    if (this.onEmpty) {
      // Off an empty section: the nearest item in the direction of travel.
      const at = (k) => SECTIONS.findIndex((s) => s.kind === k);
      const here = at(this.section);
      const t = step > 0
        ? items.find((x) => at(x.kind) > here)
        : [...items].reverse().find((x) => at(x.kind) < here);
      if (t) { this.onEmpty = false; this.cursor = t.id; this.cursorIndex = items.indexOf(t); this.section = t.kind; }
      this.reveal = true;
      cx.notify();
      return;
    }
    let i = items.findIndex((t) => t.id === this.cursor);
    i = i < 0 ? 0 : Math.max(0, Math.min(items.length - 1, i + step));
    this.cursor = items[i].id;
    this.cursorIndex = i;
    this.reveal = true;
    cx.notify();
  }

  // ---------- editing ----------

  startEdit(task, draft, cx) {
    this.mode = "edit";
    this.edit = task
      ? { id: task.id, parent_id: task.parent_id, kind: task.kind, field: "title" }
      : { id: null, ...draft, field: "title" };
    const src = task ?? { title: "", summary: "", description: "", waiting_on: draft.waiting_on ?? this.userName ?? "" };
    for (const f of FIELDS) this.inputs[f].set_value(src[f] ?? "");
    this.inputs.title.focus();
    this.reveal = true;
    cx.notify();
  }

  nextField(step, fromEnter, cx) {
    if (!this.edit) return;
    const i = FIELDS.indexOf(this.edit.field);
    if (fromEnter && i === FIELDS.length - 1) return this.finishEdit(cx);
    this.edit.field = FIELDS[(i + step + FIELDS.length) % FIELDS.length];
    this.inputs[this.edit.field].focus();
    cx.notify();
  }

  finishEdit(cx) {
    const e = this.edit;
    const v = Object.fromEntries(FIELDS.map((f) => [f, this.inputs[f].value().trim()]));
    if (!v.waiting_on) v.waiting_on = this.userName ?? "";
    this.mode = "nav";
    this.edit = null;
    this.h.focus();
    if (e.id === null) {
      if (v.title) {
        const args = ["add", `--kind=${e.kind}`, `--summary=${v.summary}`, `--description=${v.description}`,
          `--waiting-on=${v.waiting_on}`, "--terminal=", "--agent="];
        if (e.parent_id) args.push(`--parent=${e.parent_id}`);
        else {
          const here = this.here();
          args.push(`--space=${here ?? ""}`);
          if (here) args.push(`--space-name=${spaces().find((sp) => sp.id === here)?.name ?? ""}`);
        }
        args.push("--", v.title);
        this.mutate(cx, args, (t) => { this.cursor = t.id; this.onEmpty = false; this.section = t.kind; this.reveal = true; });
      }
    } else {
      const task = this.tasks.find((t) => t.id === e.id);
      if (task) {
        const changes = {};
        for (const f of FIELDS) if ((task[f] ?? "") !== v[f] && (f !== "title" || v.title)) changes[f] = v[f];
        if (Object.keys(changes).length) this.setFields(cx, task, changes);
      }
    }
    cx.notify();
  }

  // ---------- keys ----------

  key(e, cx) {
    const m = e.modifiers;
    const plain = !m.control && !m.alt && !m.platform;
    const ch = plain ? e.key_char ?? null : null;

    if (this.mode === "edit") {
      if (e.key === "escape") { this.finishEdit(cx); cx.stop_propagation(); }
      else if (e.key === "tab") { this.nextField(m.shift ? -1 : 1, false, cx); cx.stop_propagation(); }
      return;
    }

    const rows = this.rows();
    const task = this.current(rows);

    if (this.mode === "confirm") {
      if (ch === "y" && task) {
        this.tasks = this.tasks.filter((t) => t.id !== task.id);
        this.mutate(cx, ["delete", String(task.id)]);
      }
      this.mode = "nav";
      cx.stop_propagation();
      cx.notify();
      return;
    }

    let handled = true;
    if (ch === "j" || e.key === "down") this.move(1, cx);
    else if (ch === "k" || e.key === "up") this.move(-1, cx);
    else if (ch === "g") this.move(-1e9, cx);
    else if (ch === "G") this.move(1e9, cx);
    else if (ch === "f") {
      this.filter = next(FILTERS, this.filter, 1);
      save("filter", this.filter);
      this.reveal = true;
    } else if (ch === "l") {
      // Fold/unfold the task we're on. On a sub-task with none of its own: fold its parent and land there.
      const target = !task ? null : this.hasKids(task) ? task : this.tasks.find((x) => x.id === task.parent_id);
      if (target) {
        if (this.folded.has(target.id)) this.folded.delete(target.id);
        else this.folded.add(target.id);
        // Drop ids of deleted tasks while we're here.
        for (const id of this.folded) if (!this.tasks.some((x) => x.id === id)) this.folded.delete(id);
        save("folded", [...this.folded]);
        this.cursor = target.id;
      }
      this.reveal = true;
    } else if (ch === "w") {
      this.allSpaces = !this.allSpaces;
      save("allSpaces", this.allSpaces);
      this.reveal = true;
    } else if (ch === "]" || ch === "[") {
      this.jumpSection(rows, ch === "]" ? 1 : -1, cx);
    } else if (ch === "a" || ch === "o") {
      const kind = this.section === "question" ? "question" : "task";
      this.startEdit(null, { parent_id: null, kind }, cx);
    } else if (ch === "?") {
      openOverlay({ mod: "condoo", props: { role: "help" }, kind: "dialog", width: 560, height: 0.8 });
    } else if (ch === "r") {
      this.reload(cx);
    } else if (ch === "q" || e.key === "escape") {
      // In a block, esc and q hand the keyboard back and leave the panel open (the sidebar key
      // hides its column); as a dialog there's nothing to leave open, so they close it.
      if (props()?.role !== "panel") { const pane = focused()?.pane; if (pane) run("pane.focus", { pane }); }
      else this.closeDialog();
    } else if (!task) {
      handled = false;
    } else if (ch === " " || e.key === "space") {
      this.expanded = this.expanded === task.id ? null : task.id;
      if (this.expanded) this.markSeen(task, cx);
      this.reveal = true;
    } else if (ch === "c") {
      this.draft("comment", task, cx);
    } else if (ch === "e") {
      this.startEdit(task, null, cx);
    } else if (ch === ">") {
      if (this.folded.delete(task.id)) save("folded", [...this.folded]);
      this.startEdit(null, { parent_id: task.id, kind: task.kind, waiting_on: task.waiting_on }, cx);
    } else if (ch === "s" || ch === "S") {
      this.setFields(cx, task, { status: next(STATUSES, task.status, ch === "S" ? -1 : 1) });
    } else if (ch === "p" || ch === "P") {
      this.setFields(cx, task, { priority: next(PRIORITIES, task.priority, ch === "P" ? -1 : 1) });
    } else if (ch === "m") {
      // Moves the whole item: from a sub-task, its top-level parent.
      let root = task;
      while (root.parent_id) root = this.tasks.find((x) => x.id === root.parent_id) ?? { ...root, parent_id: null };
      const n = spaces().length + 1;
      openOverlay({ mod: "condoo", props: { role: "move", task: root.id, title: root.title, space: this.idx?.of(root) ?? null },
        kind: "dialog", width: 360, height: Math.min(520, 96 + n * 30) });
    } else if (ch === "x") {
      this.mode = "confirm";
    } else if (e.key === "enter") {
      if (task.kind === "question") this.draft("answer", task, cx);
      else if (task.terminal) run("agent.focus", { terminal: task.terminal });
      else this.startEdit(task, null, cx);
    } else {
      handled = false;
    }
    if (handled) { cx.stop_propagation(); cx.notify(); }
  }

  closeDialog() {
    setShared(DIALOG, null);
    closeOverlay(props().overlay);
  }

  // ---------- drawing ----------

  render(cx) {
    if (props()?.role === "answer-bar") return this.answerBar();
    if (props()?.role === "help") return this.help();
    if (props()?.role === "move") return this.movePicker();
    if (props()?.overlay && props()?.role !== "panel") return div();
    const t = theme();
    const c = palette(settings().dark !== false);

    // A move picker changed something: reload.
    const reloadToken = shared(RELOAD);
    if (reloadToken && reloadToken !== this.reloadToken) {
      this.reloadToken = reloadToken;
      cx.timer.after(0, (cx) => this.reload(cx));
    }

    // The focus key asked for the keyboard.
    const token = shared(FOCUS);
    if (token && token !== this.focusToken) {
      this.focusToken = token;
      if (Date.now() - token < 5000) cx.timer.after(30, () => { this.h.focus(); });
    }

    const rows = this.rows();
    const drafts = this.liveDrafts();
    const sel = this.mode === "edit" ? null : this.current(rows);
    const focusedNow = this.h.is_focused() || this.mode === "edit";
    const statusColor = (s) => ({
      discussion: c.maroon, in_progress: c.yellow, completed: c.green, cancelled: c.red,
    })[s] ?? t.foreground;
    const prioColor = (p) => ({ urgent: c.red, high: c.orange, normal: null, low: t.dim })[p];
    const now = Date.now() / 1000;
    const idx = this.idx;
    const here = this.here();
    const open = (x) => !x.parent_id && !CLOSED.has(x.status);
    const elsewhere = this.allSpaces ? 0 : this.tasks.filter((x) => open(x) && !this.inScope(x, idx)).length;

    const children = rows.map((r, i) => {
      if (r.header) {
        const n = this.tasks.filter((x) => x.kind === r.header.kind && open(x) && this.inScope(x, idx)).length;
        const here = this.mode !== "edit" && this.section === r.header.kind;
        return h_flex().id(`h-${r.header.kind}`).px(12).pt(i === 0 ? 8 : 16).pb(6).gap(6).items_center()
          .text_size(10.5).font_semibold().text_color(here ? t.muted : t.dim)
          .child(div().child(r.header.label.toUpperCase()))
          .child(div().text_color(t.muted).child(n ? String(n) : ""));
      }
      if (r.divider) {
        return h_flex().id(`d-${r.divider}`).mx(12).mt(6).mb(4).gap(8).items_center()
          .child(div().flex_1().h(1).bg(t.border))
          .child(div().flex_none().text_size(10).text_color(t.dim).child(r.label))
          .child(div().flex_1().h(1).bg(t.border));
      }
      if (r.empty) {
        const on = this.onEmpty && this.mode !== "edit" && r.empty.kind === this.section;
        return div().id(`e-${r.empty.kind}`).mx(6).px(8).py(6).rounded(6).text_size(11.5).text_color(t.dim)
          .when(on, (el) => el.bg(focusedNow ? t.selected : t.hover))
          .child(r.empty.empty);
      }
      if (r.draft || (this.mode === "edit" && r.task && r.task.id === this.edit.id)) {
        return this.form(t, r.depth, r.draft ? `d-${i}` : `f-${r.task.id}`);
      }
      const task = r.task;
      const selected = sel && task.id === sel.id;
      const days = Math.max(0, Math.floor((now - task.status_since) / 86400));
      const pc = prioColor(task.priority);
      const confirming = selected && this.mode === "confirm";
      // Sub-tasks are just their title until hovered, selected or opened.
      const full = r.depth === 0 || selected || this.hovered === task.id || this.expanded === task.id;
      const tsp = r.depth === 0 ? idx.of(task) : null;
      const spaceTag = tsp === DETACHED ? `was: ${task.space_name || "a closed space"}`
        : this.allSpaces && tsp && tsp !== here ? (idx.names.get(tsp) ?? "closed space") : null;
      return h_flex().id(`t-${task.id}`).mx(6).my(1).rounded(6).items_stretch()
        .when(Boolean(selected), (el) => el.bg(focusedNow ? t.selected : t.hover))
        .hover((s) => (selected ? s : s.bg(t.hover)))
        .when(r.context, (el) => el.opacity(0.45))
        .on_hover((on, cx) => {
          if (on) this.hovered = task.id;
          else if (this.hovered === task.id) this.hovered = null;
          cx.notify();
        })
        .on_mouse_down("left", () => this.h.focus())
        .on_click((_e, cx) => {
          if (r.context) return;
          this.cursor = task.id;
          if (this.mode === "confirm") this.mode = "nav";
          cx.notify();
        })
        .child(div().w(3).flex_none().rounded(2).my(4).bg(pc ?? "#00000000"))
        .child(v_flex().flex_1().min_w(0).pl(8 + r.depth * 14).pr(10).py(full ? 6 : 3).gap(2)
          .child(h_flex().gap(8).items_center()
            .when(r.depth > 0, (el) => el.child(div().text_color(t.dim).text_size(11).child("↳")))
            .child(div().flex_none().text_size(11).text_color(t.dim).child(`#${task.id}`))
            .child(div().flex_1().min_w(0).truncate().text_size(TITLE_SIZE).font_medium()
              .text_color(t.foreground).when(task.status === "cancelled", (el) => el.line_through())
              .child(task.title || "(untitled)"))
            .when(this.folded.has(task.id) && this.hasKids(task), (el) => el.child(div().flex_none().px(5).rounded(4)
              .bg(t.raised).text_size(10.5).text_color(t.muted)
              .child(`+${this.tasks.filter((x) => x.parent_id === task.id).length}`)))
            .when(drafts.has(`answer:${task.id}`) || drafts.has(`comment:${task.id}`), (el) => el.child(
              h_flex().flex_none().gap(3).px(5).rounded(4).bg(t.raised).text_size(10.5).text_color(t.blocked)
                .child("✎").child(drafts.has(`answer:${task.id}`) ? "answer open" : "comment open")))
            .when(task.comments?.length > 0, (el) => el.child(svg("mods/condoo/megaphone.svg").size(13).flex_none()
              .text_color(t.ansi3).opacity(unread(task) ? 1 : 0.4)))
            .when(full, (el) => el.child(div().flex_none().opacity(0.4).text_size(11).text_color(t.foreground).child(`${days}d`))))
          .when(full && Boolean(task.summary), (el) => el.child(
            div().text_size(TITLE_SIZE - 2).text_color(t.muted).child(task.summary)))
          .when(full && Boolean(task.answer), (el) => el.child(
            h_flex().gap(6).text_size(TITLE_SIZE - 2).text_color(t.dim)
              .child(div().flex_none().child("↪"))
              .child(div().flex_1().min_w(0).truncate().child(task.answer.split("\n")[0]))))
          .when(full, (el) => el.child(h_flex().gap(8).items_center().text_size(11)
            .when(Boolean(spaceTag), (el) => el.child(div().flex_none().px(5).rounded(4).bg(t.raised)
              .text_color(t.muted).child(spaceTag)))
            .child(div().flex_1().min_w(0).truncate()
              .text_color(statusColor(task.status))
              .when(task.status === "up_next", (el) => el.opacity(0.4))
              .child(STATUS_LABEL[task.status] ?? task.status))
            .child(div().flex_none().text_color(c.yellow).child(task.waiting_on || ""))))
          .when(this.expanded === task.id, (el) => el.child(
            v_flex().mt(6).p(8).rounded(6).bg(t.background).border_1().border_color(t.border)
              .text_size(TITLE_SIZE - 2).text_color(t.foreground)
              .child(task.description
                ? TextView.markdown(`desc-${task.id}`, task.description).selectable(true)
                : div().text_color(t.dim).child("No description. Press e to add one."))))
          .when(this.expanded === task.id, (el) => el.child(this.thread(t, task, now)))
          .when(confirming, (el) => el.child(
            div().pt(4).text_size(11.5).text_color(c.red).child(`Delete "${task.title}"${this.hasKids(task) ? " and its sub-tasks" : ""}? (y/n)`))));
    });

    // Keep the cursor on screen.
    if (this.reveal) {
      this.reveal = false;
      const idx = rows.findIndex((r) => (sel && r.task?.id === sel.id) || r.draft
        || (this.onEmpty && r.empty?.kind === this.section)
        || (this.mode === "edit" && r.task && r.task.id === this.edit?.id));
      if (idx >= 0) this.scroll.scroll_to_item(idx);
    }

    const filterLabel = this.filter === "all" ? "All" : this.filter ? STATUS_LABEL[this.filter] : "Open";
    const filterColor = this.filter === "all" ? t.foreground : this.filter ? statusColor(this.filter) : t.dim;

    return v_flex().id("condoo-root").size_full().bg(t.chrome).text_color(t.foreground)
      .track_focus(this.h).on_key_down((e, cx) => this.key(e, cx))
      .on_mouse_down("left", () => { if (this.mode !== "edit") this.h.focus(); })
      .child(h_flex().flex_none().h(42).px(12).gap(8).items_center().border_b_1().border_color(t.border)
        .child(div().text_size(13).font_semibold().text_color(focusedNow ? t.foreground : t.muted).child("Condoo"))
        .child(div().flex_1())
        .child(h_flex().gap(6).text_size(11)
          .child(div().px(6).py(1).rounded(4).when(this.allSpaces, (el) => el.bg(t.raised))
            .text_color(this.allSpaces ? t.foreground : t.dim)
            .child(this.allSpaces ? "w: all spaces" : `w: this space${elsewhere ? ` (+${elsewhere})` : ""}`))
          .child(div().px(6).py(1).rounded(4).when(Boolean(this.filter), (el) => el.bg(t.raised))
            .text_color(filterColor).child(`f: ${filterLabel}`))))
      .when(Boolean(this.error), (el) => el.child(
        div().px(12).py(8).text_size(11.5).text_color(c.red).child(`Couldn't read Condoo: ${this.error}`)))
      .child(div().flex_1().min_h(0).relative()
        .child(v_flex().id("condoo-list").size_full().overflow_y_scrollbar().track_scroll(this.scroll).pb(8)
          .children(this.loaded ? children : [div().p(12).text_size(11.5).text_color(t.dim).child("Loading…")])));
  }

  // [ and ]: the first item of the previous/next section that has any (wrapping round).
  // Empty sections count too, so `a` can add to them.
  jumpSection(rows, step, cx) {
    const items = this.selectable(rows);
    const kind = next(SECTIONS.map((s) => s.kind), this.section, step);
    const first = items.find((t) => t.kind === kind);
    this.section = kind;
    this.onEmpty = !first;
    this.cursor = first ? first.id : null;
    if (first) this.cursorIndex = items.indexOf(first);
    this.reveal = true;
    cx.notify();
  }

  // ---------- drafts: $EDITOR on an answer or comment ----------
  //
  // The editor is a background terminal labelled with what it drafts (DRAFT_LABEL "answer:ID")
  // and where (PATH_LABEL). The dialog only shows it: closing the dialog (Esc, another mod)
  // leaves the editor running, the item shows "draft open", and Enter / c opens it again.
  // The draft is saved when the editor exits. A panel that starts (or reloads) takes over
  // editors left running, so an answer is never lost with the copy that started it.

  // Editors still running, from their labels: "answer:40" → {kind, id, addr, path}.
  liveDrafts() {
    const out = new Map();
    for (const term of terminals()) {
      const key = term.labels?.[DRAFT_LABEL];
      const m = /^(answer|comment):(\d+)$/.exec(key ?? "");
      if (m && term.state === "live") {
        out.set(key, { kind: m[1], id: Number(m[2]), addr: term.address, path: term.labels[PATH_LABEL] });
      }
    }
    return out;
  }

  // Enter on a question, c on any item: open its editor again if it's running, else start one.
  draft(kind, task, cx) {
    const key = `${kind}:${task.id}`;
    const running = this.liveDrafts().get(key);
    if (running) return this.showDraft(running, task, cx);
    if (this.starting.has(key)) return;
    this.starting.add(key);
    cx.spawn(async (cx) => {
      try {
        // condoo writes the draft and picks the editor ($VISUAL, $EDITOR, else one it finds),
        // so no shell is needed and Windows works too.
        const { path, editor } = await cli([`${kind}-file`, String(task.id)]);
        const r = await hc(["run", "--json", "--bg", "--no-parent", "--cwd", home(),
          "--label", `${DRAFT_LABEL}=${key}`, "--label", `${PATH_LABEL}=${path}`, "--", ...editor, path]);
        const addr = JSON.parse(r.stdout || "{}").address;
        if (!addr) throw new Error(r.stderr.trim() || "couldn't start the editor");
        const d = { kind, id: task.id, addr, path };
        this.showDraft(d, task, cx);
        this.starting.delete(key);
        await this.awaitDraft(d, task, cx);
      } catch (e) {
        this.starting.delete(key);
        notify({ title: `Condoo: ${kind === "answer" ? "answering" : "commenting"} failed`, body: String(e.message ?? e), level: "error", timeout: 6000 });
      }
    });
  }

  // The dialog over the editor. Opening it again replaces any earlier one for the same editor.
  showDraft(d, task, cx) {
    const old = this.dialogs.get(d.addr);
    if (old) closeOverlay(old);
    const bar = d.kind === "answer"
      ? { label: "Answer", agent: task?.agent && `for ${task.agent}` }
      : { label: "Comment", agent: `on #${d.id}` };
    const term = terminals().find((x) => x.address === d.addr);
    this.dialogs.set(d.addr, openOverlay({
      kind: "dialog", modal: true, width: 0.6, height: 0.6,
      layout: { type: "column", children: [
        { type: "block", size: { px: 30 }, content: { mod: "condoo",
          props: { role: "answer-bar", ...bar, title: task?.title ?? `#${d.id}`, terminal: d.addr, editor: term?.argv?.[0] ?? "" } } },
        { type: "block", content: { terminal: d.addr } },
      ] },
    }));
    cx.timer.after(150, () => run("pane.focus", { terminal: d.addr }));
    cx.notify();
  }

  // Waits for the editor to exit, then saves the draft and tells the item's agent.
  async awaitDraft(d, task, cx) {
    this.waiting.add(d.addr);
    for (;;) {
      await cx.sleep(400);
      const term = terminals().find((x) => x.address === d.addr);
      if (!term || term.state !== "live") break;
    }
    this.waiting.delete(d.addr);
    const dialog = this.dialogs.get(d.addr);
    this.dialogs.delete(d.addr);
    if (dialog) closeOverlay(dialog);
    task = this.tasks.find((x) => x.id === d.id) ?? task ?? { id: d.id, title: `#${d.id}` };
    try {
      if (!d.path) throw new Error(`no draft path for #${d.id}`);
      await (d.kind === "answer" ? this.saveAnswer(task, d.path) : this.saveComment(task, d.path));
    } catch (e) {
      notify({ title: `Condoo: saving the ${d.kind} failed`, body: String(e.message ?? e), level: "error", timeout: 6000 });
    }
    this.h.focus();
    this.reload(cx);
  }

  // Editors left running by an earlier copy of the panel (a reload): wait on them here.
  adoptDrafts(cx) {
    for (const d of this.liveDrafts().values()) {
      if (this.waiting.has(d.addr)) continue;
      cx.spawn(async (cx) => this.awaitDraft(d, null, cx));
    }
  }

  // Types a message into the item's agent if it's still running and idle. Returns
  // "sent", "busy" (saved, not typed) or null (no agent to tell).
  async tellAgent(task, msg) {
    if (!task.terminal || !terminals().some((x) => x.address === task.terminal)) return null;
    const sent = await hc(["send", "--if", "idle", task.terminal, msg]);
    return sent.code === 0 ? "sent" : "busy";
  }

  // Enter on a question: store the answer and hand it to the agent.
  async saveAnswer(task, path) {
    const out = await cli(["answer-save", String(task.id), path]);
    const who = task.agent || task.terminal;
    if (out.cancelled) {
      return notify({ title: "No answer saved", body: task.title, level: "info", timeout: 3000 });
    }
    const told = await this.tellAgent(task, `Answer to your question (condoo #${task.id}) "${task.title}":\n\n${out.answer}`);
    notify(told === "sent" ? { title: `Answer sent to ${who}`, body: task.title, level: "success", timeout: 3000 }
      : told === "busy" ? { title: "Answer saved, not sent", body: `${who} is busy. It can read it with condoo get ${task.id}.`, level: "warning", timeout: 6000 }
      : { title: "Answer saved", body: task.title, level: "success", timeout: 3000 });
  }

  // c: a comment, posted as the person and typed into the item's agent if it's idle.
  async saveComment(task, path) {
    const out = await cli(["comment-save", String(task.id), path]);
    const who = task.agent || task.terminal;
    if (out.cancelled) {
      return notify({ title: "No comment posted", body: task.title, level: "info", timeout: 3000 });
    }
    this.expanded = task.id;
    const told = await this.tellAgent(task,
      `Comment from ${out.author} on condoo #${task.id} "${task.title}":\n\n${out.body}`);
    notify(told === "sent" ? { title: `Comment sent to ${who}`, body: task.title, level: "success", timeout: 3000 }
      : told === "busy" ? { title: "Comment saved, not sent", body: `${who} is busy. It can read it with condoo get ${task.id}.`, level: "warning", timeout: 6000 }
      : { title: "Comment saved", body: task.title, level: "success", timeout: 3000 });
    await cli(["seen", String(task.id)]);
  }

  // Opening an item reads its comments.
  markSeen(task, cx) {
    if (!unread(task)) return;
    task.seen_at = Date.now() / 1000;
    this.mutate(cx, ["seen", String(task.id)]);
  }

  // The comments under an expanded item: the latest few, oldest first.
  thread(t, task, now) {
    const all = task.comments ?? [];
    const hidden = this.allComments === task.id ? 0 : Math.max(0, all.length - SHOWN_COMMENTS);
    const shown = all.slice(hidden);
    const hint = div().text_size(11).text_color(t.dim).child(all.length ? "c comment" : "No comments. Press c to add one.");
    if (!all.length) return v_flex().mt(8).child(hint);
    return v_flex().mt(10).gap(8)
      .child(div().text_size(10.5).font_semibold().text_color(t.dim).child("COMMENTS"))
      .when(hidden > 0, (el) => el.child(div().id(`more-${task.id}`).text_size(11).text_color(t.accent)
        .hover((s) => s.underline())
        .on_click((_e, cx) => { this.allComments = task.id; cx.stop_propagation(); cx.notify(); })
        .child(`show ${hidden} earlier`)))
      .children(shown.map((c) => v_flex().gap(2)
        .child(h_flex().gap(6).items_center().text_size(11)
          .child(div().flex_none().text_color(c.by_agent ? t.muted : t.accent).child("●"))
          .child(div().flex_1().min_w(0).truncate().font_medium()
            .text_color(c.by_agent ? t.muted : t.accent).child(c.author || (c.by_agent ? "agent" : "you")))
          .child(div().flex_none().text_color(t.dim).child(ago(Math.max(0, now - c.created_at)))))
        .child(div().pl(14).text_size(TITLE_SIZE - 2).text_color(t.foreground)
          .child(TextView.markdown(`c-${c.id}`, c.body).selectable(true)))))
      .child(hint);
  }

  answerBar() {
    const t = theme();
    const p = props();
    return h_flex().id("answer-bar").size_full().px(12).gap(10).items_center().bg(t.chrome)
      .border_b_1().border_color(t.border).text_size(12)
      .child(div().flex_none().font_semibold().text_color(t.muted).child(p.label ?? "Answer"))
      .child(div().flex_1().min_w(0).truncate().text_color(t.foreground).child(p.title ?? ""))
      .when(Boolean(p.agent), (el) => el.child(div().flex_none().text_color(t.dim).child(p.agent)))
      .child(div().flex_none().text_color(t.dim).child(
        /(^|[\\/])n?vim?(\.exe)?$/i.test(p.editor ?? "") ? ":wq saves · :q! cancels" : "save and quit the editor to send"));
  }

  // However the help closes (its keys, Herdr's Esc, a click beside it), give the panel the keyboard back.
  async close(_cx) {
    if (props()?.role === "help" || props()?.role === "move") setShared(FOCUS, Date.now());
    if (props()?.role === "panel") setShared(DIALOG, null);
  }

  // m: where to move an item. Open spaces in Herdr's order, then Personal (no space).
  moveTargets() {
    return [
      ...spaces().map((sp) => ({ id: sp.id, name: sp.name, machine: sp.machine })),
      { id: "", name: "Personal", note: "every space" },
    ];
  }

  movePicker() {
    const t = theme();
    const p = props();
    const targets = this.moveTargets();
    this.pick = Math.max(0, Math.min(targets.length - 1, this.pick));
    const choose = (target, cx) => {
      cx.spawn(async (_cx) => {
        try {
          await cli(["move", String(p.task), `--space=${target.id}`, `--space-name=${target.id ? target.name : ""}`]);
          setShared(RELOAD, Date.now());
        } catch (e) {
          notify({ title: "Condoo: move failed", body: String(e.message ?? e), level: "error", timeout: 5000 });
        }
        closeOverlay(p.overlay);
      });
    };
    return v_flex().id("condoo-move").size_full().min_h(0).bg(t.popover).text_color(t.foreground)
      .track_focus(this.h)
      .on_key_down((e, cx) => {
        const ch = e.key_char ?? null;
        if (ch === "j" || e.key === "down") this.pick = Math.min(targets.length - 1, this.pick + 1);
        else if (ch === "k" || e.key === "up") this.pick = Math.max(0, this.pick - 1);
        else if (e.key === "enter") return (choose(targets[this.pick], cx), cx.stop_propagation());
        else if (e.key === "escape" || ch === "q") return (closeOverlay(p.overlay), cx.stop_propagation());
        else return;
        cx.stop_propagation();
        cx.notify();
      })
      .child(v_flex().flex_none().px(16).pt(14).pb(10).gap(2).border_b_1().border_color(t.border)
        .child(div().text_size(11).font_semibold().text_color(t.muted).child("MOVE TO"))
        .child(div().text_size(13).truncate().child(p.title || "(untitled)")))
      .child(v_flex().id("condoo-move-list").flex_1().min_h(0).overflow_y_scrollbar().p(6)
        .children(targets.map((target, i) => {
          const current = (target.id || null) === (p.space === DETACHED ? undefined : p.space);
          return h_flex().id(`mv-${i}`).h(30).px(10).gap(8).items_center().rounded(6).text_size(12.5)
            .when(i === this.pick, (el) => el.bg(t.selected))
            .hover((s) => (i === this.pick ? s : s.bg(t.hover)))
            .on_click((_e, cx) => choose(target, cx))
            .child(div().flex_1().min_w(0).truncate().child(target.name))
            .when(Boolean(target.machine), (el) => el.child(div().flex_none().text_size(11).text_color(t.dim).child(target.machine)))
            .when(Boolean(target.note), (el) => el.child(div().flex_none().text_size(11).text_color(t.dim).child(target.note)))
            .when(current, (el) => el.child(div().flex_none().text_size(11).text_color(t.accent).child("now")));
        })))
      .child(div().flex_none().px(16).py(8).border_t_1().border_color(t.border).text_size(11).text_color(t.dim)
        .child("j k move · enter moves it and its sub-tasks · esc cancels"));
  }

  help() {
    const t = theme();
    const close = () => closeOverlay(props().overlay);
    const section = ([title, intro, rows]) => v_flex().gap(6)
      .child(div().text_size(10.5).font_semibold().text_color(t.muted).child(title.toUpperCase()))
      .when(Boolean(intro), (el) => el.child(div().text_size(11.5).text_color(t.dim).child(intro)))
      .children(rows.map(([k, what], i) => h_flex().id(`${title}-${i}`).gap(12).items_start().text_size(12)
        .child(div().w(110).flex_none().font_medium().text_color(t.accent).child(k.replace("{toggle}", toggleKey())))
        .child(div().flex_1().min_w(0).text_color(t.foreground).child(what))));
    return v_flex().id("condoo-help").size_full().min_h(0).bg(t.popover).text_color(t.foreground)
      .track_focus(this.h)
      .on_key_down((e, cx) => {
        if (e.key === "escape" || e.key_char === "?" || e.key_char === "q") { close(); cx.stop_propagation(); }
      })
      .child(h_flex().flex_none().h(44).px(18).items_center().border_b_1().border_color(t.border)
        .child(div().flex_1().text_size(15).font_semibold().child("Condoo keys"))
        .child(div().text_size(11).text_color(t.dim).child("esc / ? closes")))
      .child(v_flex().id("condoo-help-body").flex_1().min_h(0).overflow_y_scrollbar().px(18).py(14).gap(18)
        .children(HELP.map(section)));
  }

  hasKids(task) {
    return this.tasks.some((x) => x.parent_id === task.id);
  }

  form(t, depth, id) {
    const field = (f) => {
      const active = this.edit?.field === f;
      return v_flex().gap(3)
        .child(div().text_size(10.5).text_color(t.dim).child(FIELD_LABEL[f]))
        .child((f === "description" ? Textarea.new(this.inputs[f]).py(6) : Input.new(this.inputs[f]).h(f === "title" ? 28 : 26))
          .px(8).rounded(6).border_1()
          .border_color(active ? t.accent : t.input).bg(t.background)
          .text_size(f === "title" ? TITLE_SIZE : TITLE_SIZE - 2).text_color(t.foreground));
    };
    const title = this.edit?.id === null ? (this.edit.parent_id ? "New sub-task" : "New task") : "Edit";
    return v_flex().id(id).mx(6).my(2).ml(6 + depth * 14).p(10).gap(8).rounded(8)
      .bg(t.surface).border_1().border_color(t.accent)
      .child(h_flex().gap(8).items_center()
        .child(div().flex_1().text_size(11).font_semibold().text_color(t.muted).child(title))
        .child(div().flex_none().text_size(10).text_color(t.dim).child("tab next · esc save")))
      .children(FIELDS.map(field));
  }
}
