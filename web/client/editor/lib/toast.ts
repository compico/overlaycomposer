import type { Ui } from "./ui.ts";

/** Всплывающее сообщение внизу экрана (`<div popover id="toast">`) и показ ошибок. */
export function createToaster(t: HTMLElement): Ui {
  let timer = 0;
  const toast = (msg: string) => {
    t.textContent = msg;
    // popover попадает в top layer — иначе тост прячется за открытой модалкой (<dialog>.showModal())
    try { if (!t.matches(":popover-open")) t.showPopover(); } catch { /* старый браузер: обычный fixed */ }
    t.classList.add("show");
    clearTimeout(timer);
    timer = setTimeout(() => {
      t.classList.remove("show");
      setTimeout(() => { try { if (!t.classList.contains("show")) t.hidePopover(); } catch { /* ignore */ } }, 250);
    }, 2200) as unknown as number;
  };
  return {
    toast,
    fail(e) {
      console.error(e);
      toast(`Error: ${e instanceof Error ? e.message : String(e)}`);
    },
  };
}
