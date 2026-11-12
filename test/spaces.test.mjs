// Tests for mods/condoo/spaces.js: which space an item shows in, and what the panel stores
// to keep up with renamed and reopened spaces. Run: node --test test/
import { test } from "node:test";
import assert from "node:assert/strict";
import { spaceIndex, DETACHED, machineOf } from "../mods/condoo/spaces.js";

const space = (id, name, tabs = []) => ({ id, name, tabs: tabs.map((t) => ({ id: t })) });
const READY = [{ name: null, layout: "ready" }];

test("machineOf", () => {
  assert.equal(machineOf("sp1"), null);
  assert.equal(machineOf("vps/sp1"), "vps");
  assert.equal(machineOf(null), null);
  assert.equal(machineOf(""), null);
});

test("an item follows its agent's terminal first", () => {
  const idx = spaceIndex([space("a", "api", ["tab-a"]), space("b", "web", ["tab-b"])],
    [{ terminal: "t1", tab: "tab-b" }], READY);
  assert.equal(idx.of({ terminal: "t1", space: "a", space_name: "api" }), "b");
});

test("then its stored space, if open", () => {
  const idx = spaceIndex([space("a", "api")], [], READY);
  assert.equal(idx.of({ terminal: "t-gone", space: "a", space_name: "api" }), "a");
});

test("no space is personal", () => {
  const idx = spaceIndex([space("a", "api")], [], READY);
  assert.equal(idx.of({ space: null }), null);
  assert.equal(idx.of({}), null);
  assert.equal(idx.fix({ space: null }), null);
});

test("a closed space detaches its items", () => {
  const idx = spaceIndex([space("a", "api")], [], READY);
  assert.equal(idx.of({ space: "gone", space_name: "condoo" }), DETACHED);
  assert.equal(idx.of({ space: "gone", space_name: null }), DETACHED, "an old item without a name");
  assert.equal(idx.fix({ space: "gone", space_name: "condoo" }), null, "nothing to reattach to");
});

test("a space opened again with the same name takes its items back", () => {
  const idx = spaceIndex([space("new-id", "condoo"), space("x", "api")], [], READY);
  const item = { space: "old-id", space_name: "condoo" };
  assert.equal(idx.of(item), "new-id");
  assert.deepEqual(idx.fix(item), { space: "new-id" }, "the new id is stored");
});

test("with two spaces of that name, the first one wins", () => {
  const idx = spaceIndex([space("one", "condoo"), space("two", "condoo")], [], READY);
  assert.equal(idx.of({ space: "gone", space_name: "condoo" }), "one");
});

test("a renamed space updates the stored name", () => {
  const idx = spaceIndex([space("a", "api v2")], [], READY);
  assert.deepEqual(idx.fix({ space: "a", space_name: "api" }), { space_name: "api v2" });
  assert.equal(idx.fix({ space: "a", space_name: "api v2" }), null, "already current");
  assert.deepEqual(idx.fix({ space: "a", space_name: null }), { space_name: "api v2" }, "old item gains a name");
});

test("names only match on the same machine", () => {
  const spaces = [space("local-1", "condoo"), space("vps/sp9", "condoo")];
  const machines = [{ name: null, layout: "ready" }, { name: "vps", layout: "ready" }];
  const idx = spaceIndex(spaces, [], machines);
  assert.equal(idx.of({ space: "vps/old", space_name: "condoo" }), "vps/sp9");
  assert.equal(idx.of({ space: "old", space_name: "condoo" }), "local-1");
  const onlyLocal = spaceIndex([space("local-1", "condoo")], [], machines);
  assert.equal(onlyLocal.of({ space: "vps/old", space_name: "condoo" }), DETACHED,
    "a remote item doesn't move to a local space with the same name");
});

test("nothing is stored while the item's machine is loading or offline", () => {
  const spaces = [space("new-id", "condoo"), space("vps/sp9", "web")];
  for (const layout of ["loading", "unavailable"]) {
    const idx = spaceIndex(spaces, [], [{ name: null, layout }, { name: "vps", layout: "ready" }]);
    assert.equal(idx.fix({ space: "old-id", space_name: "condoo" }), null, `local ${layout}`);
    assert.deepEqual(idx.fix({ space: "vps/sp9", space_name: "old" }), { space_name: "web" }, "vps is ready");
  }
  const vpsDown = spaceIndex(spaces, [], [{ name: null, layout: "ready" }, { name: "vps", layout: "unavailable" }]);
  assert.equal(vpsDown.of({ space: "vps/sp1", space_name: "api" }), DETACHED, "still shown, under Detached Spaces");
  assert.equal(vpsDown.fix({ space: "vps/sp1", space_name: "api" }), null);
});

test("before Herdr has loaded any spaces, nothing is changed", () => {
  const idx = spaceIndex([], [], [{ name: null, layout: "loading" }]);
  assert.equal(idx.of({ space: "a", space_name: "api" }), DETACHED);
  assert.equal(idx.fix({ space: "a", space_name: "api" }), null);
});
