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
}

const ROLES: [TwitchRole, string][] = [["streamer", "Streamer"], ["bot", "Bot"]];

export function mountTwitch(refs: TwitchRefs, model: TwitchModel, ui: Ui) {
  let formFilled = false;

  model.subscribe((v) => {
    refs.box.hidden = !v;
    if (!v) return;
    refs.redirect.textContent = v.redirectUri;
    if (!formFilled) { // поля заполняем один раз, чтобы опрос не затирал ввод
      refs.clientId.value = v.clientId;
      refs.clientSecret.placeholder = v.hasSecret ? "saved — leave empty to keep" : "client secret";
      formFilled = true;
    }
    refs.appHint.textContent = v.clientId && v.hasSecret ? "" : "Enter Client ID and Client Secret, then connect accounts.";
    const ready = !!v.clientId && v.hasSecret;
    refs.accounts.replaceChildren(...ROLES.map(([role, title]) => {
      const acc = v.accounts.find((a) => a.role === role);
      const problem = acc ? accountProblem(acc) : "";
      const info = el("span", {
        class: problem ? "hint warn" : "hint",
        textContent: acc ? `${title}: ${acc.login}${problem ? ` — ${problem}` : ""}` : `${title}: not connected`,
      });
      // Два аккаунта в одном браузере не подключить (Twitch помнит вход), поэтому вместо перехода — ссылка в буфер обмена:
      // её можно открыть в другом браузере/профиле/приватном окне, где выполнен вход нужным аккаунтом.
      const connect = el("button", {
        type: "button", class: "iconbtn", disabled: !ready,
        title: acc ? `Copy a link to reconnect the ${title.toLowerCase()} account` : `Copy the authorization link for the ${title.toLowerCase()} account`,
      });
      connect.setAttribute("aria-label", connect.title);
      connect.innerHTML = COPY_ICON; // статическая разметка без пользовательских данных
      connect.addEventListener("click", async () => {
        try {
          const url = await model.connectLink(role);
          const ok = await copyText(url);
          ui.toast(ok
            ? `${title} link copied. Open it in a browser where the ${title.toLowerCase()} Twitch account is signed in (a private window works). It is valid for 10 minutes.`
            : "Could not access the clipboard — copy the link from the prompt");
          if (!ok) prompt("Authorization link (Ctrl+C):", url);
        } catch (e) { ui.fail(e); }
      });
      const btns = el("span", { class: "row" }, connect);
      if (acc) {
        const off = el("button", { type: "button", textContent: "Disconnect" });
        off.addEventListener("click", async () => {
          if (!confirm(`Disconnect Twitch account “${acc.login}”?`)) return;
          try { await model.disconnect(role); } catch (e) { ui.fail(e); }
        });
        btns.append(off);
      }
      return el("div", { class: "row", style: "justify-content:space-between" }, info, btns);
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
