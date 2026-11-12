// Which Herdr space each Condoo item belongs to. Pure: it takes Herdr's live data as
// arguments, so it can be tested without Herdr (test/spaces.test.mjs).

// The item's space is closed (shown under Detached Spaces, in every space).
export const DETACHED = "\u0000detached";

// Remote space ids are "<machine>/<id>"; this computer's have no prefix (null).
export const machineOf = (id) => (id && id.includes("/") ? id.slice(0, id.indexOf("/")) : null);

// spaces: herdr spaces(); panes: herdr panes(); machines: herdr machines().
//
// of(item) says where an item shows:
//   1. where its agent's terminal is now, if it's still open;
//   2. the space stored with it, if that's open;
//   3. an open space on the same machine with the stored name (a space closed and opened
//      again gets a new id but usually the same name: its repo or folder);
//   4. otherwise DETACHED.
//   null: the item has no space (Personal tasks, shown in every space).
//
// fix(item) says what to store so the item keeps up: the open space's current name, or its
// new id after a close and reopen. null when nothing changes, and always null while the
// item's machine hasn't loaded its spaces (loading, offline), since then a missing space
// isn't necessarily closed.
export function spaceIndex(spaces, panes, machines) {
  const tabSpace = new Map();
  const names = new Map();
  const byName = new Map(); // "<machine>|<name>" -> the first open space called that
  for (const sp of spaces) {
    names.set(sp.id, sp.name);
    const key = `${machineOf(sp.id) ?? ""}|${sp.name}`;
    if (!byName.has(key)) byName.set(key, sp.id);
    for (const tab of sp.tabs ?? []) tabSpace.set(tab.id, sp.id);
  }
  const termSpace = new Map();
  for (const p of panes) if (p.terminal) termSpace.set(p.terminal, tabSpace.get(p.tab));
  const ready = new Set(machines.filter((m) => m.layout === "ready").map((m) => m.name ?? ""));
  const sameName = (t) => (t.space_name ? byName.get(`${machineOf(t.space) ?? ""}|${t.space_name}`) : undefined);

  const of = (t) => {
    const live = t.terminal && termSpace.get(t.terminal);
    if (live) return live;
    if (!t.space) return null;
    if (names.has(t.space)) return t.space;
    return sameName(t) ?? DETACHED;
  };
  const fix = (t) => {
    if (!t.space || !ready.has(machineOf(t.space) ?? "")) return null;
    if (names.has(t.space)) return names.get(t.space) !== t.space_name ? { space_name: names.get(t.space) } : null;
    const again = sameName(t);
    return again ? { space: again } : null;
  };
  return { names, of, fix };
}
