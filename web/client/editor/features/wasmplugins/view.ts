import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import {
  buildConfig, configFields, formValues, missingGrants, needsAttention, permissionText, toggleGrant, twitchLine, validateNewReward,
  type ConfigField, type Reward, type WasmInfo, type WasmModel, type WasmPlugin,
} from "./model.ts";

export interface WasmRefs {
  openBtn: HTMLElement;
  dialog: HTMLDialogElement;
  body: HTMLElement;
  reloadBtn: HTMLElement;
  folderBtn: HTMLElement;
  status: HTMLElement;
}

export interface WasmPorts {
  ui: Ui;
  openFolder(): Promise<unknown>;
}

// Всё, что пришло от плагина (имя, описание, ошибка, схема настроек), попадает в DOM только через textContent/el().
export function mountWasmPlugins(refs: WasmRefs, model: WasmModel, ports: WasmPorts) {
  const { ui } = ports;
  const drafts = new Map<string, Record<string, string | boolean>>();
  let rewards: Reward[] | null = null;
  let rewardsError = "";

  async function loadRewards() {
    try { rewards = await model.rewards(); rewardsError = ""; } catch (e) { rewards = []; rewardsError = e instanceof Error ? e.message : String(e); }
    render(model.get());
  }

  function rewardField(p: WasmPlugin, f: ConfigField, draft: Record<string, string | boolean>): HTMLElement {
    const sel = el("select", { style: "flex:1;min-width:0" });
    const cur = String(draft[f.key] ?? "");
    const opts = [el("option", { value: "", textContent: rewards ? "— not selected —" : "loading…" })];
    for (const r of rewards ?? []) opts.push(el("option", { value: r.id, textContent: `${r.title} (${r.cost})${r.manageable ? "" : " — created elsewhere"}` }));
    if (cur && !(rewards ?? []).some((r) => r.id === cur)) opts.push(el("option", { value: cur, textContent: `${cur} (not found)` }));
    sel.append(...opts);
    sel.value = cur;
    sel.addEventListener("change", () => { draft[f.key] = sel.value; });
    const create = el("button", { type: "button", textContent: "New reward…" });
    const wrap = el("div", {}, el("div", { class: "row" }, sel, create));
    const note = rewardsError || (rewards?.length && sel.value && rewards.find((r) => r.id === sel.value)?.manageable === false
      ? "This reward was not created by Overlay Composer: redemptions can be read but not marked fulfilled/cancelled." : "");
    if (note) wrap.append(el("div", { class: "hint warn", textContent: note }));
    create.addEventListener("click", () => {
      if (wrap.querySelector(".newreward")) return;
      const title = el("input", { type: "text", placeholder: "Title", value: "Заказать трек" });
      const cost = el("input", { type: "number", min: "1", value: "500", style: "width:90px" });
      const prompt = el("input", { type: "text", placeholder: "Prompt shown to viewers", value: "Ссылка на трек Яндекс Музыки" });
      const input = el("input", { type: "checkbox", checked: true });
      const ok = el("button", { type: "button", textContent: "Create" });
      ok.addEventListener("click", async () => {
        const r = { title: title.value, cost: Number(cost.value), prompt: prompt.value, inputRequired: input.checked };
        const bad = validateNewReward(r);
        if (bad) { ui.toast(bad); return; }
        try {
          const made = await model.createReward(r);
          rewards = [...(rewards ?? []), made];
          draft[f.key] = made.id;
          ui.toast(`Reward “${made.title}” created. Save the settings to use it.`);
          render(model.get());
        } catch (e) { ui.fail(e); }
      });
      wrap.append(el("div", { class: "newreward", style: "margin-top:6px;display:grid;gap:4px" }, title, el("div", { class: "row" }, cost, prompt),
        el("label", { class: "row" }, input, "Viewer must enter text"), el("div", { class: "row" }, ok)));
    });
    void p;
    return wrap;
  }

  function configForm(p: WasmPlugin): HTMLElement | null {
    const fields = configFields(p.meta.configSchema);
    if (fields.length === 0) return null;
    let draft = drafts.get(p.meta.id);
    if (!draft) { draft = formValues(fields, p.config); drafts.set(p.meta.id, draft); }
    const d = draft;
    const box = el("div", { style: "display:grid;gap:6px;margin-top:6px" });
    for (const f of fields) {
      let input: HTMLElement;
      if (f.kind === "reward") {
        input = rewardField(p, f, d);
      } else if (f.kind === "boolean") {
        const cb = el("input", { type: "checkbox", checked: d[f.key] === true });
        cb.addEventListener("change", () => { d[f.key] = cb.checked; });
        input = cb;
      } else {
        const inp = el("input", { type: f.kind === "number" ? "number" : "text", value: String(d[f.key] ?? ""), spellcheck: false });
        inp.addEventListener("input", () => { d[f.key] = inp.value; });
        input = inp;
      }
      box.append(el("label", { style: "display:grid;gap:2px" }, el("span", { textContent: f.title }),
        input, ...(f.description ? [el("span", { class: "hint", textContent: f.description })] : [])));
    }
    const save = el("button", { type: "button", textContent: "Save settings" });
    save.addEventListener("click", async () => {
      const r = buildConfig(fields, d);
      if (!r.config) { ui.toast(r.error ?? "Invalid settings"); return; }
      try { await model.setConfig(p.meta.id, r.config); drafts.delete(p.meta.id); ui.toast("Settings saved"); } catch (e) { ui.fail(e); }
    });
    box.append(el("div", { class: "row" }, save));
    return box;
  }

  function card(p: WasmPlugin): HTMLElement {
    const m = p.meta;
    const warn = needsAttention(p);
    const state = el("span", { class: `badge${p.state === "running" ? "" : " bad"}`, textContent: p.state });
    const head = el("div", { class: "row", style: "justify-content:space-between" },
      el("strong", { textContent: `${m.name} ${m.version}` }), state);
    const kids: (Node | string)[] = [head];
    kids.push(el("div", { class: "hint", textContent: [m.id, m.author, p.file].filter(Boolean).join(" · ") }));
    if (m.description) kids.push(el("div", { textContent: m.description }));
    if (p.lastError) kids.push(el("div", { class: "hint warn", textContent: `${p.lastError}${p.restarts ? ` (restarts: ${p.restarts})` : ""}` }));
    const perms = m.permissions ?? [];
    if (perms.length) {
      kids.push(el("div", { class: "hint", style: "margin-top:6px", textContent: "Permissions" }));
      for (const perm of perms) {
        const cb = el("input", { type: "checkbox", checked: p.granted.includes(perm) });
        cb.addEventListener("change", async () => {
          cb.disabled = true;
          try { await model.setGrants(m.id, toggleGrant(p, perm, cb.checked)); } catch (e) { ui.fail(e); render(model.get()); }
        });
        kids.push(el("label", { class: "row", title: perm }, cb, el("span", { textContent: permissionText(perm) })));
      }
      const miss = missingGrants(p);
      if (miss.length) kids.push(el("div", { class: "hint warn", textContent: "The plugin does not work until you allow what it asks for." }));
    }
    const form = configForm(p);
    if (form) kids.push(form);
    const restart = el("button", { type: "button", textContent: "Restart" });
    restart.addEventListener("click", async () => { try { await model.restart(m.id); } catch (e) { ui.fail(e); } });
    kids.push(el("div", { class: "row", style: "margin-top:6px" }, restart));
    return el("div", { class: "migration", style: warn ? "" : "border-left-color:var(--line)" }, ...kids);
  }

  function render(v: WasmInfo | null) {
    refs.openBtn.hidden = !v;
    if (!v) return;
    const first = v.dirs[0] ?? "";
    refs.status.textContent = `${first ? `Folder: ${first}\n` : ""}${twitchLine(v.twitch, v.plugins.some((p) => (p.meta.permissions ?? []).includes("twitch.redemption") && p.granted.includes("twitch.redemption")))}`;
    const kids: HTMLElement[] = [];
    if (v.plugins.length === 0) kids.push(el("div", { class: "hint", textContent: "No standalone plugins yet. Put a .wasm file into the folder above and press Rescan." }));
    for (const p of v.plugins) kids.push(card(p));
    for (const pr of v.problems) kids.push(el("div", { class: "hint warn", textContent: `${pr.file}: ${pr.error}` }));
    refs.body.replaceChildren(...kids);
    if (rewards === null && v.plugins.some((p) => configFields(p.meta.configSchema).some((f) => f.kind === "reward"))) { rewards = []; void loadRewards(); }
  }

  model.subscribe(render);
  refs.openBtn.addEventListener("click", async () => {
    rewards = null;
    drafts.clear();
    await model.load();
    refs.dialog.showModal();
  });
  refs.reloadBtn.addEventListener("click", async () => { try { await model.reload(); ui.toast("Rescanned"); } catch (e) { ui.fail(e); } });
  refs.folderBtn.addEventListener("click", () => { ports.openFolder().catch(ui.fail); });
  return { open: () => refs.openBtn.click() };
}
