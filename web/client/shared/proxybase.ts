/**
 * Адрес, с которого раздаётся прокси сайтов источников.
 *
 * Если сервер изолировал прокси на отдельном порту (он сообщает его в <meta name="oc-proxy-port">), это другой origin:
 * JS чужого сайта, открытого через прокси, не достаёт до API приложения. Иначе (изоляция выключена proxyPort=-1 в config.json
 * или порт был занят) — пустая строка: прокси на том же origin, как раньше.
 */
export function proxyBase(
  doc: Pick<Document, "querySelector"> = document,
  loc: Pick<Location, "protocol" | "hostname"> = location,
): string {
  const port = Number(doc.querySelector('meta[name="oc-proxy-port"]')?.getAttribute("content"));
  if (!Number.isInteger(port) || port < 1 || port > 65535) return "";
  return `${loc.protocol}//${loc.hostname}:${port}`;
}
