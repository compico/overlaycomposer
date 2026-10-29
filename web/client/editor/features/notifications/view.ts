// Представление центра уведомлений (ванильный DOM). Получает модель и «порты» — всё, что нужно снаружи.
import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import type { NotificationsModel, NotifItem, PluginChange } from "./model.ts";

/** Элементы, которыми владеет представление (во фреймворке — собственная разметка компонента). */
export interface NotificationsRefs {
  bell: HTMLElement;
  count: HTMLElement;
  panel: HTMLElement;
  dialog: HTMLDialogElement;
  detail: HTMLElement;
  actions: HTMLElement;
}

export interface NotificationsPorts {
  ui: Ui;
  /** Открыть магазин плагинов (кнопка в окне обновления плагина). */
  openStore(): void;
  /** Открыть настройки (кнопка в уведомлении «переподключите аккаунт Twitch»). */
  openSettings(): void;
}

function ago(ts: number): string {
  const d = Math.max(0, Date.now() / 1000 - ts);
  if (d < 90) return "just now";
  if (d < 5400) return `${Math.round(d / 60)} min ago`;
  if (d < 129600) return `${Math.round(d / 3600)} h ago`;
  return new Date(ts * 1000).toLocaleDateString();
}

const textList = (items: string[]) => el("ul", {}, ...items.map((t) => el("li", {}, t)));

export function mountNotifications(refs: NotificationsRefs, model: NotificationsModel, ports: NotificationsPorts) {
  const { ui } = ports;
  const panelOpen = () => refs.panel.matches(":popover-open");

  function renderBell(unread: number) {
    refs.count.hidden = unread === 0;
    refs.count.textContent = String(unread);
  }

  function renderPanel() {
    const s = model.get();
    const body = s.items.length
      ? s.items.map((n) => {
          const b = el("button", { type: "button", class: "notif" + (n.read ? "" : " unread") }, n.title);
          if (n.text) b.append(el("small", {}, n.text));
          b.append(el("small", {}, ago(n.at)));
          b.addEventListener("click", () => openDetail(n));
          return b;
        })
      : [el("div", { class: "hint", style: "padding:8px" }, "No notifications.")];
    const check = el("button", { type: "button" }, "Check plugin updates");
    check.addEventListener("click", async () => {
      check.disabled = true;
      try {
        await model.check();
        ui.toast(model.get().items.length ? "Check finished" : "Everything is up to date");
      } catch (e) { ui.fail(e); } finally { check.disabled = false; }
    });
    const readAll = el("button", { type: "button" }, "Mark all read");
    readAll.addEventListener("click", () => void model.markRead("").catch(ui.fail));
    const clear = el("button", { type: "button", class: "danger" }, "Clear");
    clear.addEventListener("click", () => void model.dismiss("").catch(ui.fail));
    const last = s.lastCheck ? `Plugins checked ${ago(s.lastCheck)}` : "Plugins not checked yet";
    refs.panel.replaceChildren(
      ...body,
      el("div", { class: "hint", style: "margin:6px 2px" }, last),
      el("div", { class: "row" }, check, readAll, clear),
    );
  }

  function openDetail(n: NotifItem) {
    const d = (n.data ?? {}) as Record<string, any>;
    const close = el("button", { value: "close" }, "Close");
    refs.detail.replaceChildren(el("strong", {}, n.title));
    refs.actions.replaceChildren();
    if (n.kind === "plugin-update") {
      refs.detail.append(el("div", { class: "hint" }, `${d.repo ?? ""} — ${d.from ?? "?"} → ${d.to ?? "?"}`));
      if (Array.isArray(d.addedPermissions) && d.addedPermissions.length) {
        refs.detail.append(el("div", { class: "hint warn" }, `The new version asks for additional permissions: ${d.addedPermissions.join(", ")}. You will be asked to allow them.`));
      }
      const log = (Array.isArray(d.changelog) ? d.changelog : []) as PluginChange[];
      if (!log.length) refs.detail.append(el("div", { class: "hint" }, "The author did not describe this update."));
      for (const c of log) {
        refs.detail.append(el("h4", {}, c.date ? `${c.version} — ${c.date}` : c.version));
        if (c.changes?.length) refs.detail.append(textList(c.changes));
        if (c.migration) refs.detail.append(el("div", { class: "hint" }, "What to do after updating:"), el("div", { class: "migration" }, c.migration));
      }
      const open = el("button", { type: "button", class: "primary" }, "Open plugin store");
      open.addEventListener("click", () => { refs.dialog.close(); ports.openStore(); });
      refs.actions.append(open);
    } else if (n.kind === "app-update") {
      refs.detail.append(el("div", { class: "hint" }, `${d.current ?? "?"} → ${d.latest ?? "?"}`));
      if (d.notes) refs.detail.append(el("h4", {}, "Release notes"), el("div", { class: "migration", style: "border-left-color:var(--line)" }, String(d.notes)));
      if (typeof d.url === "string" && /^https:\/\/github\.com\//.test(d.url)) {
        const a = el("a", { href: d.url, target: "_blank", rel: "noopener noreferrer", textContent: "Open the release page" });
        refs.detail.append(el("div", { style: "margin-top:8px" }, a));
      }
    } else if (n.kind === "twitch-reconnect") {
      if (n.text) refs.detail.append(el("div", { class: "hint warn" }, n.text));
      refs.detail.append(el("div", { class: "hint" }, "Settings → Twitch accounts: copy the link next to the account and open it in a browser where that Twitch account is signed in."));
      const open = el("button", { type: "button", class: "primary" }, "Open Settings");
      open.addEventListener("click", () => { refs.dialog.close(); ports.openSettings(); });
      refs.actions.append(open);
    } else if (n.text) {
      refs.detail.append(el("div", { class: "hint" }, n.text));
    }
    const dismiss = el("button", { type: "button", class: "danger" }, "Dismiss");
    dismiss.addEventListener("click", () => { refs.dialog.close(); void model.dismiss(n.key).catch(ui.fail); });
    refs.actions.append(dismiss, close);
    if (panelOpen()) refs.panel.hidePopover();
    refs.dialog.showModal();
    if (!n.read) void model.markRead(n.key).catch(() => {});
  }

  model.subscribe((s) => {
    renderBell(s.unread);
    if (panelOpen()) renderPanel();
  });

  refs.bell.addEventListener("click", () => {
    if (panelOpen()) { refs.panel.hidePopover(); return; }
    const r = refs.bell.getBoundingClientRect();
    refs.panel.style.top = `${r.bottom + 6}px`;
    refs.panel.style.left = `${Math.max(8, Math.min(r.right - 360, window.innerWidth - 368))}px`;
    renderPanel();
    refs.panel.showPopover();
    void model.load();
  });

  void model.load();
  setInterval(() => void model.load(), 30000);
}
