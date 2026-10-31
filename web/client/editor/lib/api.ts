// Клиент HTTP API сервера. Модели зависят от интерфейса `Api`, а не от fetch: в тестах подставляется фейк,
// при смене фреймворка клиент остаётся тем же.

/** Ошибка ответа сервера: status нужен, чтобы отличить конфликт версий (409) от остальных сбоев. */
export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(message: string, status: number, body: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

/** 409: layout изменили после того, как мы его прочитали. */
export const isConflict = (e: unknown): boolean => e instanceof ApiError && e.status === 409;

export type Api = <T = unknown>(path: string, method?: string, body?: unknown) => Promise<T>;

/** Реальный клиент. Заголовок x-overlay-composer нужен серверу для изменяющих запросов (защита от чужих страниц). */
export const httpApi: Api = async <T = unknown>(path: string, method = "GET", body?: unknown): Promise<T> => {
  const res = await fetch(path, {
    method,
    headers: body !== undefined
      ? { "content-type": "application/json", "x-overlay-composer": "1" }
      : { "x-overlay-composer": "1" },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const e = await res.json().catch(() => ({ error: res.statusText }));
    throw new ApiError(e.error ?? res.statusText, res.status, e);
  }
  return res.json() as Promise<T>;
};
