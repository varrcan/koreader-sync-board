import type { StatisticsBucket, StatisticsCalendar, StatisticsPeriod, StatisticsTotals } from "@ksb/api-client";
import { Clock } from "lucide-react";
import { type ReactNode, useLayoutEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import { formatDuration, parseDate } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Штриховка дня без данных — из макета ThemeKit §10. */
const hatch =
  "border border-b-0 border-dashed border-input bg-[repeating-linear-gradient(135deg,transparent_0_5px,color-mix(in_oklch,var(--muted-foreground)_22%,transparent)_5px_6px)]";

export function HatchSwatch() {
  return <span className={cn("size-3 shrink-0 rounded-[2px] border-b", hatch)} />;
}

function localDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** Снимок старше сегодняшнего дня (to — «сегодня» в поясе запроса). */
export function isStale(asOf: string | null, to: string): boolean {
  return asOf !== null && localDay(new Date(asOf)) < parseDate(to).getTime();
}

/** Отметка «статистика на 2 ноября» у заголовка блока. */
export function AsOfMark({ asOf }: { asOf: string }) {
  const { t, i18n } = useTranslation();
  const date = new Intl.DateTimeFormat(i18n.language, { day: "numeric", month: "long" }).format(new Date(asOf));
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground [&_svg]:size-3">
      <Clock />
      {t("stats.asOf", { date })}
    </span>
  );
}

export function NoDataHint() {
  const { t } = useTranslation();
  return (
    <p className="flex items-center gap-2 text-xs text-pretty text-muted-foreground">
      <HatchSwatch />
      {t("stats.noDataHint")}
    </p>
  );
}

/** Скорость, страниц в час; null при нулевом времени. */
export function speed(t: StatisticsTotals): number | null {
  return t.duration > 0 ? Math.round((t.pages / t.duration) * 3600) : null;
}

export type Change = { text: string; up: boolean };

/**
 * Изменение к прошлому периоду: время, страницы и скорость — в процентах, сессии —
 * разницей. Если в периоде есть дни без данных, сравнивать нечестно: вместо изменения —
 * «за 5 из 7 дней».
 */
export function useChange(series: StatisticsBucket[], period: StatisticsPeriod) {
  const { t, i18n } = useTranslation();
  const missing = series.filter((b) => b.no_data).length;
  return (cur: number | null, prev: number | null, absolute = false): Change => {
    if (missing > 0) {
      const known = { days: series.length - missing, total: series.length };
      return { text: t(period === "year" ? "stats.partialMonths" : "stats.partial", known), up: false };
    }
    if (cur === null || prev === null || prev === 0) {
      return { text: cur ? t("stats.noPrevious") : t(`stats.same.${period}`), up: false };
    }
    const diff = absolute ? cur - prev : Math.round(((cur - prev) / prev) * 100);
    if (diff === 0) return { text: t(`stats.same.${period}`), up: false };
    const value = absolute
      ? new Intl.NumberFormat(i18n.language).format(Math.abs(diff))
      : new Intl.NumberFormat(i18n.language, { style: "percent" }).format(Math.abs(diff) / 100);
    return { text: `${diff > 0 ? "↑" : "↓"} ${value} ${t(`stats.vs.${period}`)}`, up: diff > 0 };
  };
}

export function ChangeText({ change, className }: { change: Change; className?: string }) {
  return (
    <span className={cn("text-xs tabular-nums", change.up ? "text-chart-5" : "text-muted-foreground", className)}>
      {change.text}
    </span>
  );
}

export function StatTile({ label, value, change }: { label: string; value: ReactNode; change: Change }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5 rounded-xl border bg-card p-4">
      <div className="text-[13px] text-muted-foreground">{label}</div>
      <div className="text-[clamp(22px,6vw,28px)] leading-8 font-semibold tracking-[-0.01em] tabular-nums">{value}</div>
      <ChangeText change={change} />
    </div>
  );
}

function bucketLabel(b: StatisticsBucket, monthly: boolean, count: number, lang: string): string {
  const d = parseDate(b.date);
  if (monthly) return new Intl.DateTimeFormat(lang, { month: "short" }).format(d);
  if (count <= 7) return new Intl.DateTimeFormat(lang, { weekday: "short" }).format(d);
  return String(d.getDate());
}

/**
 * Столбцы по дням или месяцам. Последний столбец — сегодня (текущий месяц) и рисуется
 * полным цветом; дни без данных — штриховкой, а не нулём. Высота — по родителю:
 * график растягивается, если соседняя карточка выше.
 */
export function DayBars({
  series,
  monthly,
  values,
  className,
}: {
  series: StatisticsBucket[];
  monthly: boolean;
  /** Подписи значений над столбцами — для недели. */
  values?: boolean;
  className?: string;
}) {
  const { i18n } = useTranslation();
  const lang = i18n.language;
  const top = Math.max(1, ...series.map((b) => b.duration));
  // Подписи под столбцами месяца — через одну, чтобы не слипались.
  const every = series.length > 14 ? Math.ceil(series.length / 10) : 1;
  const gap = series.length > 14 ? "gap-[3px]" : "gap-[clamp(6px,2cqw,16px)]";
  return (
    <div className={cn("flex min-h-40 flex-col gap-1.5 @container", className)}>
      <div className={cn("flex min-h-32 flex-1 items-end border-b", gap)}>
        {series.map((b, i) => {
          const today = i === series.length - 1;
          const date = new Intl.DateTimeFormat(
            lang,
            monthly ? { month: "long", year: "numeric" } : { dateStyle: "medium" },
          ).format(parseDate(b.date));
          const title = `${date} · ${formatDuration(b.duration, lang)}`;
          return (
            <div key={b.date} className="flex h-full min-w-0 flex-1 flex-col items-center justify-end gap-1" title={title}>
              {values && !b.no_data && (
                <span className="text-[11px] whitespace-nowrap text-muted-foreground tabular-nums">
                  {b.duration > 0 ? formatDuration(b.duration, lang, true) : "—"}
                </span>
              )}
              <div
                className={cn(
                  "w-full max-w-11 rounded-t-[4px]",
                  b.no_data
                    ? cn("h-[86%]", hatch)
                    : b.duration === 0
                      ? "h-0.5 bg-muted"
                      : today
                        ? "bg-chart-1"
                        : "bg-chart-1/55",
                )}
                style={b.no_data || b.duration === 0 ? undefined : { height: `max(4px, ${(b.duration / top) * 86}%)` }}
              />
            </div>
          );
        })}
      </div>
      <div className={cn("flex", gap)}>
        {series.map((b, i) => (
          <span
            key={b.date}
            className={cn(
              "min-w-0 flex-1 text-center text-[11px] text-muted-foreground tabular-nums",
              i === series.length - 1 && !b.no_data && "font-semibold text-foreground",
              // 12 месяцев на телефоне не помещаются — через один, от текущего.
              monthly && (series.length - 1 - i) % 2 === 1 && "max-sm:invisible",
            )}
          >
            {(series.length - 1 - i) % every === 0 ? bucketLabel(b, monthly, series.length, lang) : ""}
          </span>
        ))}
      </div>
    </div>
  );
}

/** Распределение времени по часу начала записи. */
export function HourBars({ hours }: { hours: number[] }) {
  const { i18n } = useTranslation();
  const top = Math.max(1, ...hours);
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex h-36 items-end gap-[3px] border-b">
        {hours.map((v, h) => (
          <div
            key={h}
            title={`${h}:00 · ${formatDuration(v, i18n.language)}`}
            className={cn("min-w-0 flex-1 rounded-t-[2px]", v > 0 ? "bg-chart-2" : "h-0.5 bg-muted")}
            style={v > 0 ? { height: `max(2px, ${(v / top) * 100}%)` } : undefined}
          />
        ))}
      </div>
      <div className="flex justify-between text-[10px] text-muted-foreground tabular-nums">
        {[0, 6, 12, 18, 23].map((h) => (
          <span key={h}>{h}</span>
        ))}
      </div>
    </div>
  );
}

/** Час, с которого начинается больше всего чтения; null без чтения. */
export function peakHour(hours: number[]): number | null {
  const top = Math.max(...hours);
  return top > 0 ? hours.indexOf(top) : null;
}

const heatLevels = ["bg-heat-0", "bg-heat-1", "bg-heat-2", "bg-heat-3", "bg-heat-4"];

/** Уровень клетки: нет чтения, до 15 минут, до 30, до часа, час и больше. */
function heatLevel(seconds: number): number {
  if (seconds <= 0) return 0;
  if (seconds < 15 * 60) return 1;
  if (seconds < 30 * 60) return 2;
  if (seconds < 3600) return 3;
  return 4;
}

function isoDate(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** Календарь 53 недели × 7 дней с понедельника; на узком экране прокручен к свежим неделям. */
export function Heatmap({ calendar }: { calendar: StatisticsCalendar }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [calendar]);

  const byDay = new Map(calendar.days.map((d) => [d.date, d.duration]));
  const from = parseDate(calendar.from);
  const to = parseDate(calendar.to).getTime();
  const weeks: { days: (Date | null)[]; month: string | null }[] = [];
  for (let w = 0; ; w++) {
    const start = new Date(from.getFullYear(), from.getMonth(), from.getDate() + w * 7);
    if (start.getTime() > to) break;
    const days = Array.from({ length: 7 }, (_, i) => {
      const d = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i);
      return d.getTime() > to ? null : d;
    });
    // Подпись месяца — над неделей, в которой он начинается.
    const first = days.find((d) => d?.getDate() === 1);
    const month = w === 0 || first ? new Intl.DateTimeFormat(lang, { month: "short" }).format(first ?? start) : null;
    weeks.push({ days, month });
  }
  // Неполный первый месяц не подписывается, если следующий начинается через неделю-две.
  if (weeks[0] && (weeks[1]?.month || weeks[2]?.month)) weeks[0].month = null;
  const weekdays = [0, 2, 4].map((i) =>
    new Intl.DateTimeFormat(lang, { weekday: "short" }).format(new Date(from.getFullYear(), from.getMonth(), from.getDate() + i)),
  );

  return (
    <div className="flex flex-col gap-2.5">
      <div ref={scroller} className="overflow-x-auto pb-1">
        <div className="flex w-max gap-2">
          <div className="sticky left-0 z-10 grid grid-rows-[repeat(7,11px)] gap-[3px] bg-card pt-[15px] pr-1 text-[10px] leading-[11px] text-muted-foreground">
            <span>{weekdays[0]}</span>
            <span />
            <span>{weekdays[1]}</span>
            <span />
            <span>{weekdays[2]}</span>
          </div>
          <div className="flex gap-[3px]">
            {weeks.map((w, i) => (
              <div key={i} className="flex flex-col gap-[3px]">
                <span className="h-3 w-[11px] overflow-visible text-[10px] leading-3 whitespace-nowrap text-muted-foreground">
                  {w.month}
                </span>
                {w.days.map((d, j) =>
                  d ? (
                    <div
                      key={j}
                      title={`${d.toLocaleDateString(lang)} · ${formatDuration(byDay.get(isoDate(d)) ?? 0, lang)}`}
                      className={cn("size-[11px] rounded-[2px]", heatLevels[heatLevel(byDay.get(isoDate(d)) ?? 0)])}
                    />
                  ) : (
                    <div key={j} className="size-[11px]" />
                  ),
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
      <div className="flex items-center justify-end gap-1 text-[11px] text-muted-foreground">
        <span className="mr-1">{t("stats.less")}</span>
        {heatLevels.map((c) => (
          <span key={c} className={cn("size-[11px] rounded-[2px]", c)} />
        ))}
        <span className="ml-1">{t("stats.more")}</span>
      </div>
    </div>
  );
}
