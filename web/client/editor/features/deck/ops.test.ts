import { test } from "node:test";
import assert from "node:assert/strict";
import {
  addGroup, addTile, findSpot, groupWidth, moveGroup, moveTile, removeGroup, removeTile, renameGroup, sizesFor, tileTitle, unusedScenes, updateTile,
  type DeckGrid, type DeckLayout,
} from "./ops.ts";

const grid: DeckGrid = { rows: 4, maxGroupCols: 6, sizes: [{ w: 1, h: 1 }, { w: 2, h: 1 }, { w: 3, h: 1 }, { w: 2, h: 3 }] };
const base = (): DeckLayout => ({ rev: 1, groups: [{ id: "g1", name: "A", tiles: [] }] });
const S11 = { w: 1, h: 1 }, S21 = { w: 2, h: 1 }, S23 = { w: 2, h: 3 };

test("addTile ставит плитки столбцами сверху вниз", () => {
  let l = base();
  const ids: string[] = [];
  for (let i = 0; i < 5; i++) {
    const r = addTile(l, "g1", S21, { scene: `s${i}` }, grid)!;
    l = r.layout;
    ids.push(r.id);
  }
  const pos = l.groups[0].tiles.map((t) => [t.x, t.y]);
  assert.deepEqual(pos, [[0, 0], [0, 1], [0, 2], [0, 3], [2, 0]]);
  assert.deepEqual(ids, ["t1", "t2", "t3", "t4", "t5"]);
  assert.equal(groupWidth(l.groups[0]), 4);
});

test("addTile: неизвестный размер, нет группы, нет места", () => {
  const l = base();
  assert.equal(addTile(l, "g1", { w: 5, h: 5 }, {}, grid), null);
  assert.equal(addTile(l, "nope", S11, {}, grid), null);
  const tiny: DeckGrid = { ...grid, maxGroupCols: 1 };
  const one = addTile(l, "g1", S11, {}, tiny)!.layout;
  const full = addTile(addTile(addTile(one, "g1", S11, {}, tiny)!.layout, "g1", S11, {}, tiny)!.layout, "g1", S11, {}, tiny)!.layout;
  assert.equal(addTile(full, "g1", S11, {}, tiny), null);
});

test("операции не мутируют исходную раскладку", () => {
  const l = base();
  const snap = JSON.stringify(l);
  addTile(l, "g1", S11, { scene: "x" }, grid);
  renameGroup(l, "g1", "B");
  addGroup(l);
  assert.equal(JSON.stringify(l), snap);
});

test("moveTile: границы, пересечения, другая группа", () => {
  let l = addGroup(base())!.layout;
  l = addTile(l, "g1", S21, { scene: "a" }, grid)!.layout; // t1 в (0,0)
  l = addTile(l, "g1", S11, { scene: "b" }, grid)!.layout; // t2 в (0,1)
  assert.equal(moveTile(l, "t2", "g1", 1, 0, grid), null, "пересекает t1");
  assert.equal(moveTile(l, "t1", "g1", 5, 0, grid), null, "за правой границей");
  assert.equal(moveTile(l, "t1", "g1", 0, 4, grid), null, "за нижней границей");
  assert.equal(moveTile(l, "t1", "g1", -1, 0, grid), null);
  const moved = moveTile(l, "t2", "g1", 2, 0, grid)!;
  assert.deepEqual([moved.groups[0].tiles[1].x, moved.groups[0].tiles[1].y], [2, 0]);
  const cross = moveTile(l, "t1", "g2", 0, 0, grid)!;
  assert.equal(cross.groups[0].tiles.length, 1);
  assert.equal(cross.groups[1].tiles[0].id, "t1");
  // плитку можно «подвинуть» на её же место (не пересекается сама с собой)
  assert.ok(moveTile(l, "t1", "g1", 1, 0, grid));
});

test("updateTile: размер только если помещается; пустые значения убираются", () => {
  let l = addTile(base(), "g1", S11, { scene: "a", label: "A", color: "red" }, grid)!.layout;
  l = addTile(l, "g1", S11, { scene: "b" }, grid)!.layout; // t2 в (0,1)
  assert.equal(updateTile(l, "t1", { w: 2, h: 3 }, grid), null, "упирается в t2");
  assert.equal(updateTile(l, "t1", { w: 4, h: 1 }, grid), null, "нет такого размера");
  const wide = updateTile(l, "t1", { w: 3, h: 1 }, grid)!;
  assert.equal(wide.groups[0].tiles[0].w, 3);
  const cleared = updateTile(l, "t1", { label: "", color: "", icon: "", confirm: false }, grid)!;
  const t = cleared.groups[0].tiles[0];
  assert.ok(!("label" in t) && !("color" in t) && !("icon" in t) && !("confirm" in t));
  const set = updateTile(l, "t1", { confirm: true, icon: "ban", scene: "Z", label: "x".repeat(100) }, grid)!.groups[0].tiles[0];
  assert.equal(set.confirm, true);
  assert.equal(set.icon, "ban");
  assert.equal(set.action.scene, "Z");
  assert.equal([...set.label!].length, 40);
  assert.equal(updateTile(l, "nope", { label: "x" }, grid), null);
});

test("sizesFor показывает только помещающиеся размеры", () => {
  let l = addTile(base(), "g1", S11, { scene: "a" }, grid)!.layout;
  assert.equal(sizesFor(l, "t1", grid).length, 4);
  l = addTile(l, "g1", S11, { scene: "b" }, grid)!.layout; // под ней
  assert.deepEqual(sizesFor(l, "t1", grid).map((s) => `${s.w}x${s.h}`), ["1x1", "2x1", "3x1"]);
});

test("группы: добавить, переименовать, переставить, удалить; предел", () => {
  let l = base();
  const a = addGroup(l, "B")!;
  assert.equal(a.id, "g2");
  l = a.layout;
  l = renameGroup(l, "g2", "x".repeat(50) + "\n")!;
  assert.equal([...l.groups[1].name!].length, 24);
  l = moveGroup(l, "g2", -1)!;
  assert.deepEqual(l.groups.map((g) => g.id), ["g2", "g1"]);
  assert.equal(moveGroup(l, "g2", -1), null);
  assert.equal(moveGroup(l, "g1", 1), null);
  l = removeGroup(l, "g2")!;
  assert.deepEqual(l.groups.map((g) => g.id), ["g1"]);
  assert.equal(removeGroup(l, "zz"), null);
  let many = base();
  for (let i = 0; i < 15; i++) many = addGroup(many)!.layout;
  assert.equal(many.groups.length, 16);
  assert.equal(addGroup(many), null);
});

test("id плиток не повторяются после удаления", () => {
  let l = addTile(base(), "g1", S11, {}, grid)!.layout;
  l = addTile(l, "g1", S11, {}, grid)!.layout;
  l = removeTile(l, "t1")!;
  const r = addTile(l, "g1", S11, {}, grid)!;
  assert.equal(r.id, "t1");
  assert.equal(new Set(r.layout.groups[0].tiles.map((t) => t.id)).size, 2);
  assert.equal(removeTile(l, "nope"), null);
});

test("unusedScenes и findSpot для 2×3", () => {
  const l = addTile(base(), "g1", S23, { scene: "a" }, grid)!.layout;
  assert.deepEqual(unusedScenes(l, ["a", "b", "c"]), ["b", "c"]);
  assert.deepEqual(findSpot(l.groups[0], S23, grid), { x: 2, y: 0 });
});

test("updateTile: действие плагина берёт подпись, значок и «второе касание» из объявления, но не затирает своё", () => {
  const toggle = { plugin: "music", pluginName: "Music", id: "toggle", title: "Play / pause", icon: "play" };
  const ban = { plugin: "music", pluginName: "Music", id: "skip-ban", title: "Skip and ban", icon: "ban", danger: true };
  let l = addTile(base(), "g1", S11, { scene: "Game", label: "" }, grid)!.layout;
  l = updateTile(l, "t1", { pluginAction: toggle }, grid)!;
  let t1 = l.groups[0].tiles[0];
  assert.deepEqual(t1.action, { type: "plugin", plugin: "music", pluginAction: "toggle" });
  assert.equal(t1.label, "Play / pause");
  assert.equal(t1.icon, "play");
  assert.equal(t1.confirm, undefined);
  assert.equal(tileTitle(t1), "Play / pause");
  // своя подпись остаётся; опасное действие включает подтверждение
  l = updateTile(l, "t1", { label: "Next!" }, grid)!;
  l = updateTile(l, "t1", { pluginAction: ban }, grid)!;
  t1 = l.groups[0].tiles[0];
  assert.deepEqual([t1.label, t1.icon, t1.confirm, t1.action.pluginAction], ["Next!", "play", true, "skip-ban"]);
  // вернуть сцену: плагин из действия убирается
  l = updateTile(l, "t1", { scene: "Chat" }, grid)!;
  assert.deepEqual(l.groups[0].tiles[0].action, { type: "scene", scene: "Chat" });
});

test("tileTitle: подпись, сцена, плагин и действие", () => {
  const t = (action: object, label?: string) => ({ id: "x", x: 0, y: 0, w: 1, h: 1, label, action }) as never;
  assert.equal(tileTitle(t({ type: "scene", scene: "Game" })), "Game");
  assert.equal(tileTitle(t({ type: "scene", scene: "Game" }, "Mine")), "Mine");
  assert.equal(tileTitle(t({ type: "plugin", plugin: "music", pluginAction: "toggle" })), "music: toggle");
});

test("unusedScenes не считает плитки плагинов занятыми сценами", () => {
  const l = base();
  l.groups[0].tiles.push({ id: "p", x: 0, y: 0, w: 1, h: 1, action: { type: "plugin", plugin: "music", pluginAction: "toggle" } });
  assert.deepEqual(unusedScenes(l, ["A", "B"]), ["A", "B"]);
});

test("updateTile: папка со сценами", () => {
  let l = addTile(base(), "g1", S23, { scene: "Game", label: "" }, grid)!.layout;
  l = updateTile(l, "t1", { confirm: true }, grid)!;
  l = updateTile(l, "t1", { folder: ["Game", " ", "Chat", "Game"] }, grid)!;
  let t = l.groups[0].tiles[0];
  assert.deepEqual(t.action, { type: "folder", scenes: ["Game", "Chat"] });
  assert.equal(t.confirm, undefined); // папке второе касание не нужно
  assert.equal(t.label, "Scenes");
  assert.equal(tileTitle({ ...t, label: undefined }), "Folder (2)");
  // свою подпись не трогаем, пустой список и больше предела — отказ
  l = updateTile(l, "t1", { label: "Fav" }, grid)!;
  l = updateTile(l, "t1", { folder: ["A"] }, grid)!;
  t = l.groups[0].tiles[0];
  assert.deepEqual([t.label, t.action.scenes], ["Fav", ["A"]]);
  assert.equal(updateTile(l, "t1", { folder: [] }, grid), null);
  assert.equal(updateTile(l, "t1", { folder: [" "] }, grid), null);
  assert.equal(updateTile(l, "t1", { folder: Array.from({ length: 9 }, (_, i) => `s${i}`) }, grid), null);
  assert.ok(updateTile(l, "t1", { folder: Array.from({ length: 8 }, (_, i) => `s${i}`) }, grid));
  // обратно на сцену: список сцен из действия убирается
  l = updateTile(l, "t1", { scene: "Game" }, grid)!;
  assert.deepEqual(l.groups[0].tiles[0].action, { type: "scene", scene: "Game" });
});
