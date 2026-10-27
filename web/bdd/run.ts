// Мини-BDD раннер без зависимостей: читает features/*.feature (Gherkin, русские и английские ключевые слова)
// и выполняет шаги из web/bdd/steps/*.steps.ts.  Запуск: node --experimental-strip-types web/bdd/run.ts [фильтр по имени файла]
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

export type World = Record<string, any>;
type StepFn = (w: World, ...args: any[]) => void | Promise<void>;
interface StepDef { re: RegExp; fn: StepFn; src: string; types: string[] }

export class Steps {
  defs: StepDef[] = [];
  /** Шаблон с параметрами: {int} {float} {string} (в двойных кавычках) {word}. Ключевое слово (Дано/Когда/…) не указывается. */
  step(pattern: string, fn: StepFn) {
    const esc = pattern.replace(/[.*+?^$()|[\]\\]/g, "\\$&");
    const src = esc
      .replace(/\{int\}/g, "(-?\\d+)")
      .replace(/\{float\}/g, "(-?\\d+(?:\\.\\d+)?)")
      .replace(/\{string\}/g, '"([^"]*)"')
      .replace(/\{word\}/g, "(\\S+)");
    const types = [...pattern.matchAll(/\{(int|float|string|word)\}/g)].map((m) => m[1]);
    this.defs.push({ re: new RegExp(`^${src}$`), fn, src: pattern, types });
  }
}

const KW = {
  feature: /^(Функционал|Функция|Feature):\s*(.*)$/,
  background: /^(Предыстория|Контекст|Background):/,
  scenario: /^(Сценарий|Scenario):\s*(.*)$/,
  outline: /^(Структура сценария|Scenario Outline):\s*(.*)$/,
  examples: /^(Примеры|Examples):/,
  step: /^(Дано|Когда|Тогда|И|Но|Given|When|Then|And|But|\*)\s+(.*)$/,
};

interface Scenario { name: string; steps: string[] }

function parse(text: string): { name: string; background: string[]; scenarios: Scenario[] } {
  const out = { name: "", background: [] as string[], scenarios: [] as Scenario[] };
  let cur: { name: string; steps: string[]; outline: boolean; header: string[] | null; rows: string[][] } | null = null;
  let inBackground = false;
  let inExamples = false;
  const flush = () => {
    if (!cur) return;
    if (cur.outline) {
      for (const row of cur.rows) {
        const steps = cur.steps.map((s) => s.replace(/<([^>]+)>/g, (_, k) => row[cur!.header!.indexOf(k)] ?? `<${k}>`));
        out.scenarios.push({ name: `${cur.name} [${row.join(", ")}]`, steps });
      }
    } else out.scenarios.push({ name: cur.name, steps: cur.steps });
    cur = null;
  };
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#") || line.startsWith("@")) continue;
    let m: RegExpMatchArray | null;
    if ((m = line.match(KW.feature))) { out.name = m[2]; continue; }
    if (KW.background.test(line)) { flush(); inBackground = true; inExamples = false; continue; }
    if ((m = line.match(KW.outline))) { flush(); inBackground = false; inExamples = false; cur = { name: m[2], steps: [], outline: true, header: null, rows: [] }; continue; }
    if ((m = line.match(KW.scenario))) { flush(); inBackground = false; inExamples = false; cur = { name: m[2], steps: [], outline: false, header: null, rows: [] }; continue; }
    if (KW.examples.test(line)) { inExamples = true; continue; }
    if (line.startsWith("|")) {
      const cells = line.slice(1, -1).split("|").map((c) => c.trim());
      if (inExamples && cur) { if (!cur.header) cur.header = cells; else cur.rows.push(cells); }
      continue;
    }
    if ((m = line.match(KW.step))) {
      if (inBackground) out.background.push(m[2]);
      else if (cur) cur.steps.push(m[2]);
      continue;
    }
    if (!cur && !inBackground) continue; // свободное описание функционала
    throw new Error(`не понял строку: ${line}`);
  }
  flush();
  return out;
}

async function main() {
  const root = new URL("../../", import.meta.url).pathname;
  const filter = process.argv[2] ?? "";
  const steps = new Steps();
  for (const f of readdirSync(join(root, "web/bdd/steps")).filter((f) => f.endsWith(".steps.ts")).sort()) {
    const mod = await import(pathToFileURL(join(root, "web/bdd/steps", f)).href);
    mod.register(steps);
  }
  let pass = 0, fail = 0;
  const failures: string[] = [];
  for (const f of readdirSync(join(root, "features")).filter((f) => f.endsWith(".feature") && f.includes(filter)).sort()) {
    const feat = parse(readFileSync(join(root, "features", f), "utf8"));
    console.log(`\nФункционал: ${feat.name}  (${f})`);
    for (const sc of feat.scenarios) {
      const world: World = {};
      let failed = "";
      for (const text of [...feat.background, ...sc.steps]) {
        const def = steps.defs.find((d) => d.re.test(text));
        if (!def) { failed = `нет реализации шага: «${text}»`; break; }
        const args = text.match(def.re)!.slice(1).map((a, i) => (def.types[i] === "int" || def.types[i] === "float" ? Number(a) : a));
        try { await def.fn(world, ...args); } catch (e) { failed = `шаг «${text}»: ${(e as Error).message}`; break; }
      }
      if (failed) { fail++; failures.push(`${feat.name} / ${sc.name}\n    ${failed}`); console.log(`  ✗ ${sc.name}\n      ${failed}`); }
      else { pass++; console.log(`  ✓ ${sc.name}`); }
    }
  }
  console.log(`\n${pass} прошло, ${fail} упало`);
  if (fail) process.exit(1);
  if (pass === 0) { console.error("не найдено ни одного сценария"); process.exit(1); }
}

main().catch((e) => { console.error(e); process.exit(1); });
