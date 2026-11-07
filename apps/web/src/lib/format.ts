const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 86400],
  ["month", 30 * 86400],
  ["week", 7 * 86400],
  ["day", 86400],
  ["hour", 3600],
  ["minute", 60],
];

/** «5 минут назад» на языке lang; now передаётся снаружи, чтобы рендер оставался чистым. */
export function formatRelative(iso: string, lang: string, now: number): string {
  const seconds = (new Date(iso).getTime() - now) / 1000;
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: "auto" });
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return rtf.format(Math.round(seconds / size), unit);
  }
  return rtf.format(0, "minute");
}

export function formatDate(iso: string, lang: string): string {
  return new Intl.DateTimeFormat(lang, { dateStyle: "medium" }).format(new Date(iso));
}

/** «5 ч 41 мин», «38 мин», «< 1 мин»; narrow — компактно для подписей графиков. */
export function formatDuration(seconds: number, lang: string, narrow = false): string {
  const unitDisplay = narrow ? "narrow" : "short";
  const unit = (u: "hour" | "minute", n: number) =>
    new Intl.NumberFormat(lang, { style: "unit", unit: u, unitDisplay }).format(n);
  const minutes = Math.floor(seconds / 60);
  if (minutes < 1) return seconds > 0 ? `< ${unit("minute", 1)}` : unit("minute", 0);
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  if (h === 0) return unit("minute", m);
  return m === 0 ? unit("hour", h) : `${unit("hour", h)} ${unit("minute", m)}`;
}

/** Дата API (YYYY-MM-DD) как локальная полночь: new Date("2026-11-04") — полночь UTC. */
export function parseDate(date: string): Date {
  const [y = 0, m = 1, d = 1] = date.split("-").map(Number);
  return new Date(y, m - 1, d);
}

/** Короткий интервал с секундами: «45 с», «1 мин 30 с» — для среднего времени на страницу. */
export function formatSeconds(seconds: number, lang: string): string {
  const unit = (u: "minute" | "second", n: number) =>
    new Intl.NumberFormat(lang, { style: "unit", unit: u, unitDisplay: "short" }).format(n);
  const s = Math.round(seconds);
  if (s < 60) return unit("second", s);
  return s % 60 === 0 ? unit("minute", s / 60) : `${unit("minute", Math.floor(s / 60))} ${unit("second", s % 60)}`;
}
