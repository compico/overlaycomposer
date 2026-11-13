import { copyText, COPY_ICON } from "../../lib/clipboard.ts";
import { el } from "../../lib/dom.ts";
import type { Ui } from "../../lib/ui.ts";
import { accountProblem, reconnectBadge, reconnectIssues, type TwitchModel, type TwitchRole } from "./model.ts";

export interface TwitchRefs {
  box: HTMLElement;
  redirect: HTMLElement;
  clientId: HTMLInputElement;
  clientSecret: HTMLInputElement;
  appHint: HTMLElement;
  accounts: HTMLElement;
  saveApp: HTMLElement;
  resetApp: HTMLElement;
  advanced: HTMLDetailsElement;
  deviceHint: HTMLElement;
  codeHint: HTMLElement;
}

const ROLES: [TwitchRole, string][] = [["streamer", "Streamer"], ["bot", "Bot"]];

export function mountTwitch(refs: TwitchRefs, model: TwitchModel, ui: Ui) {
  let formFilled = false;
  // Идущее подключение по коду на роль: показываем код и опрашиваем сервер, пока Twitch не подтвердит.
  const pending = new Map<string, { url: string; userCode: string; flow: string; timer: number; error?: string }>();

  const stop = (role: string) => {
    const p = pending.get(role);
    if (p) clearInterval(p.timer);
    pending.delete(role);
  };

  async function startDevice(role: TwitchRole) {
    stop(role);
    const r = await model.connect(role);
    if (r.mode !== "device" || !r.flow || !r.userCode) { // сервер в режиме «по ссылке»
      await copyLink(role, r.url);
      return;
    }
    const flow = r.flow;
    const p = { url: r.url, userCode: r.userCode, flow, timer: 0 as number, error: undefined as string | undefined };
    p.timer = window.setInterval(async () => {
      const st = await model.deviceStatus(flow);
      if (!st) { stop(role); await model.load(); return; }
      if (st.status === "pending") return;
      clearInterval(p.timer);
      if (st.status === "done") { pending.delete(role); ui.toast(`Twitch account “${st.login}” connected`); } else p.error = st.error || "connection failed";
      await model.load();
    }, 2000);
    pending.set(role, p);
    window.open(r.url, "_blank", "noopener");
    await model.load();
  }

  async function copyLink(role: TwitchRole, url: string) {
    const title = ROLES.find(([r]) => r === role)![1];
    const ok = await copyText(url);
    ui.toast(ok
      ? `${title} link copied. Open it in a browser where the ${title.toLowerCase()} Twitch account is signed in (a private window works). It is valid for 10 minutes.`
      : "Could not access the clipboard — copy the link from the prompt");
    if (!ok) prompt("Authorization link (Ctrl+C):", url);
  }

  model.subscribe((v) => {
    refs.box.hidden = !v;
    if (!v) return;
    refs.redirect.textContent = v.redirectUri;
    if (!formFilled) { // поля заполняем один раз, чтобы опрос не затирал ввод
      refs.clientId.value = v.clientId;
      refs.clientSecret.placeholder = v.hasSecret ? "saved — leave empty to keep" : "client secret";
      formFilled = true;
    }
    const device = v.mode === "device";
    const ready = v.mode === "device" || v.mode === "code";
    refs.deviceHint.hidden = !device;
    refs.codeHint.hidden = device;
    refs.resetApp.hidden = !v.clientId || !v.builtIn;
    if (!ready) refs.advanced.open = true; // встроенного приложения нет: сразу показываем поля
    refs.appHint.textContent = ready ? "" : "This build has no built-in Twitch application: open “Use my own Twitch application” below and enter its Client ID.";
    refs.accounts.replaceChildren(...ROLES.map(([role, title]) => {
      const acc = v.accounts.find((a) => a.role === role);
      const problem = acc ? accountProblem(acc) : "";
      const wait = pending.get(role);
      const info = el("span", {
        class: problem ? "hint warn" : "hint",
        textContent: acc ? `${title}: ${acc.login}${problem ? ` — ${problem}` : ""}` : `${title}: not connected`,
      });
      const btns = el("span", { class: "row" });
      if (device) {
        const connect = el("button", { type: "button", disabled: !ready, textContent: acc ? "Reconnect" : "Connect" });
        connect.addEventListener("click", async () => { try { await startDevice(role); } catch (e) { ui.fail(e); } });
        btns.append(connect);
      } else {
        // Два аккаунта в одном браузере не подключить (Twitch помнит вход), поэтому вместо перехода — ссылка в буфер обмена.
        const connect = el("button", {
          type: "button", class: "iconbtn", disabled: !ready,
          title: acc ? `Copy a link to reconnect the ${title.toLowerCase()} account` : `Copy the authorization link for the ${title.toLowerCase()} account`,
        });
        connect.setAttribute("aria-label", connect.title);
        connect.innerHTML = COPY_ICON; // статическая разметка без пользовательских данных
        connect.addEventListener("click", async () => {
          try { await copyLink(role, await model.connectLink(role)); } catch (e) { ui.fail(e); }
        });
        btns.append(connect);
      }
      if (acc) {
        const off = el("button", { type: "button", textContent: "Disconnect" });
        off.addEventListener("click", async () => {
          if (!confirm(`Disconnect Twitch account “${acc.login}”?`)) return;
          try { await model.disconnect(role); } catch (e) { ui.fail(e); }
        });
        btns.append(off);
      }
      const row = el("div", { class: "row", style: "justify-content:space-between" }, info, btns);
      if (!wait) return row;
      const box = el("div", { class: "hint" });
      if (wait.error) {
        box.classList.add("warn");
        box.textContent = `${title}: ${wait.error}`;
      } else {
        const link = el("a", { href: wait.url, target: "_blank", rel: "noopener noreferrer", textContent: "twitch.tv/activate" });
        box.append(`Open `, link, ` and confirm the code `, el("b", { textContent: wait.userCode, style: "user-select:all;letter-spacing:.1em" }),
          ` — signed in as the ${title.toLowerCase()} account. Waiting for Twitch…`);
      }
      return el("div", {}, row, box);
    }));
  });

  refs.saveApp.addEventListener("click", async () => {
    try {
      await model.saveApp(refs.clientId.value, refs.clientSecret.value);
      refs.clientSecret.value = "";
      formFilled = false;
      await model.load(); // заново заполнить поля из сохранённого
      ui.toast("Twitch application saved");
    } catch (e) { ui.fail(e); }
  });

  refs.resetApp.addEventListener("click", async () => {
    if (!confirm("Forget your Twitch application and use the built-in one? Connected accounts will be disconnected.")) return;
    try {
      await model.resetApp();
      formFilled = false;
      await model.load();
      ui.toast("Using the built-in Twitch application");
    } catch (e) { ui.fail(e); }
  });

  /** Настройки открыты заново: поля формы снова заполняются с сервера. */
  return { resetForm() { formFilled = false; } };
}

/** Бейдж «Twitch: reconnect» в шапке: виден, пока какому-то аккаунту нужно переподключение; клик открывает настройки. */
export function mountTwitchBadge(badge: HTMLElement, model: TwitchModel, openSettings: () => void) {
  model.subscribe((v) => {
    const b = reconnectBadge(reconnectIssues(v));
    badge.hidden = !b;
    if (!b) return;
    badge.textContent = b.text;
    badge.title = b.title;
  });
  badge.addEventListener("click", openSettings);
}
