// Привязка layout к сценам OBS: чистая логика (что показать и как изменить), без DOM.
// Сцена принадлежит не более чем одному layout: привязка к текущему «забирает» её у другого — это решает сервер при сохранении.

export interface SceneRow {
  name: string;
  checked: boolean;
  /** Пометки: «moves from …», «used by …», «not in OBS». */
  notes: string[];
}

export interface LayoutRef { id: string; name: string; scenes: string[] }

export function sceneRows(current: LayoutRef, layouts: LayoutRef[], obsScenes: string[]): SceneRow[] {
  const owner = new Map<string, string>(); // сцена → имя другого layout, которому она принадлежит (сохранённое состояние)
  for (const l of layouts) if (l.id !== current.id) for (const s of l.scenes) owner.set(s, l.name);

  // Сцены OBS + привязанные, которых в OBS сейчас нет (переименованы/удалены/OBS не подключён).
  const names = [...obsScenes];
  for (const s of current.scenes) if (!names.includes(s)) names.push(s);

  return names.map((s) => {
    const checked = current.scenes.includes(s);
    const notes: string[] = [];
    if (owner.has(s)) notes.push(checked ? `moves from “${owner.get(s)}”` : `used by “${owner.get(s)}”`);
    if (obsScenes.length && !obsScenes.includes(s)) notes.push("not in OBS");
    return { name: s, checked, notes };
  });
}

/** Включить/выключить привязку; возвращает новый список сцен (исходный не меняется). */
export function setBound(scenes: string[], name: string, on: boolean): string[] {
  return on ? (scenes.includes(name) ? scenes : [...scenes, name]) : scenes.filter((x) => x !== name);
}

/** Добавить сцену по имени (пустое и уже добавленное игнорируются — возвращается тот же массив). */
export function addScene(scenes: string[], raw: string): string[] {
  const v = raw.trim();
  return !v || scenes.includes(v) ? scenes : [...scenes, v];
}
