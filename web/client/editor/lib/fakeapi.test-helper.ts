// Фейковый Api для тестов моделей: handlers["METHOD /path"] → ответ (или throw).
import type { Api } from "./api.ts";

export function fakeApi(handlers: Record<string, (body?: unknown) => unknown>) {
  const calls: string[] = [];
  const bodies: unknown[] = [];
  const api: Api = async <T>(path: string, method = "GET", body?: unknown) => {
    calls.push(`${method} ${path}`);
    bodies.push(body);
    const h = handlers[`${method} ${path}`];
    if (!h) throw new Error(`no handler ${method} ${path}`);
    return h(body) as T;
  };
  return { api, calls, bodies };
}
