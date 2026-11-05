import type { ActivityItem } from "@ksb/api-client";

type Named = { title: string | null; filename: string | null };

/** Название для показа: title, иначе имя файла без расширения; null у неизвестной книги. */
export function displayTitle(b: Named): string | null {
  if (b.title) return b.title;
  if (!b.filename) return null;
  const base = b.filename.slice(b.filename.search(/[^/\\]*$/));
  return base.replace(/\.[^.]+$/, "") || base;
}

/** Неизвестная — нет ни названия, ни имени файла (то же правило, что у фильтра unknown на сервере). */
export function isUnknown(b: Named): boolean {
  return b.title === null && b.filename === null;
}

// Один знак после запятой: «прочитано» на сервере — всё, что здесь показывается как 100 %.
export function formatPercent(p: number, lang: string): string {
  return new Intl.NumberFormat(lang, { style: "percent", maximumFractionDigits: 1 }).format(p);
}

export function shortHash(hash: string): string {
  return hash.length > 12 ? `${hash.slice(0, 4)}…${hash.slice(-4)}` : hash;
}

/** Имя устройства из панели; если учётка удалена — как читалка назвала себя. */
export function deviceLabel(d: { device_name: string | null; reported_device: string }): string {
  return d.device_name ?? d.reported_device;
}

export function formatTime(iso: string, lang: string): string {
  return new Intl.DateTimeFormat(lang, { hour: "2-digit", minute: "2-digit" }).format(new Date(iso));
}

function localDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

export type FeedDay = { day: Date; ago: number; items: ActivityItem[] };

/** Записи ленты по дням окончания в локальном поясе; ago — сколько дней назад (0 — сегодня). */
export function groupByDay(items: ActivityItem[], now: number): FeedDay[] {
  const today = localDay(new Date(now));
  const days: FeedDay[] = [];
  for (const item of items) {
    const day = localDay(new Date(item.ended_at));
    const last = days.at(-1);
    if (last?.day.getTime() === day) last.items.push(item);
    else days.push({ day: new Date(day), ago: Math.round((today - day) / 86400000), items: [item] });
  }
  return days;
}

export function formatDay(day: Date, lang: string, now: number): string {
  const sameYear = day.getFullYear() === new Date(now).getFullYear();
  return new Intl.DateTimeFormat(lang, {
    weekday: "short",
    day: "numeric",
    month: "long",
    year: sameYear ? undefined : "numeric",
  }).format(day);
}
