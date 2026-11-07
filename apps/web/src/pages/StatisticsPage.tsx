import type { StatisticsBook, StatisticsPeriod, StatisticsSummary } from "@ksb/api-client";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { ChartNoAxesColumn } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { helpLink } from "@/components/HelpParts";
import { StatisticsCard } from "@/components/StatisticsCard";
import {
  AsOfMark,
  DayBars,
  Heatmap,
  HourBars,
  isStale,
  NoDataHint,
  peakHour,
  speed,
  StatTile,
  useChange,
} from "@/components/StatsCharts";
import { statBookTitle } from "@/lib/books";
import { formatDuration } from "@/lib/format";
import { cn } from "@/lib/utils";
import { calendarQuery, summaryQuery } from "@/statistics";

const route = getRouteApi("/app/statistics");
const periods: StatisticsPeriod[] = ["week", "month", "year"];

function Panel({
  title,
  aside,
  className,
  children,
}: {
  title: ReactNode;
  aside?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section className={cn("flex min-w-0 flex-col gap-3 rounded-xl border bg-card p-5", className)}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h2 className="text-[15px] leading-5 font-semibold">{title}</h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

function BookRow({ book, top }: { book: StatisticsBook; top: number }) {
  const { t, i18n } = useTranslation();
  const content = (
    <>
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className={cn("truncate text-sm font-medium", !book.title && "text-muted-foreground")}>
          {statBookTitle(book, t("books.unknown"))}
        </span>
        <span className="h-1 overflow-hidden rounded-full bg-muted">
          <span className="block h-full rounded-full bg-chart-1" style={{ width: `${(book.duration / top) * 100}%` }} />
        </span>
      </span>
      <span className="flex w-28 shrink-0 flex-col items-end text-[13px] tabular-nums">
        <span className="font-medium">{formatDuration(book.duration, i18n.language)}</span>
        <span className="text-xs text-muted-foreground">{t("stats.pages", { count: book.pages })}</span>
      </span>
    </>
  );
  const row = "flex items-center gap-4 border-t py-2.5";
  if (book.has_progress && book.hash) {
    return (
      <li className="contents">
        <Link
          to="/books/$hash"
          params={{ hash: book.hash }}
          className={cn(row, "-mx-2 rounded-md px-2 hover:bg-accent")}
        >
          {content}
        </Link>
      </li>
    );
  }
  return <li className={row}>{content}</li>;
}

function Summary({ summary }: { summary: StatisticsSummary }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { totals, previous, period } = summary;
  const change = useChange(summary.series, period);
  const nf = new Intl.NumberFormat(lang);
  const cur = speed(totals);
  const peak = peakHour(summary.hours);
  const stale = summary.as_of !== null && isStale(summary.as_of, summary.to);
  const missing = summary.series.some((b) => b.no_data);

  return (
    <>
      <div className="grid max-w-5xl grid-cols-2 gap-3 md:grid-cols-[repeat(auto-fill,minmax(200px,1fr))]">
        <StatTile
          label={t("stats.tiles.time")}
          value={formatDuration(totals.duration, lang)}
          change={change(totals.duration, previous.duration)}
        />
        <StatTile
          label={t("stats.tiles.pages")}
          value={nf.format(totals.pages)}
          change={change(totals.pages, previous.pages)}
        />
        <StatTile
          label={t("stats.tiles.sessions")}
          value={nf.format(totals.sessions)}
          change={change(totals.sessions, previous.sessions, true)}
        />
        <StatTile
          label={t("stats.tiles.speed")}
          value={cur === null ? "—" : t("stats.speed", { value: nf.format(cur) })}
          change={change(cur, speed(previous))}
        />
      </div>

      <div className="flex max-w-5xl flex-wrap gap-4">
        <div className="flex flex-[2_1_420px] flex-col">
          <Panel
            className="flex-1"
            title={summary.bucket === "month" ? t("stats.byMonth") : t("stats.byDay")}
            aside={stale && summary.as_of && <AsOfMark asOf={summary.as_of} />}
          >
            <DayBars
              series={summary.series}
              monthly={summary.bucket === "month"}
              values={period === "week"}
              className="flex-1"
            />
            {missing && <NoDataHint />}
          </Panel>
        </div>
        <div className="flex flex-[1_1_300px] flex-col">
          <Panel
            className="flex-1"
            title={t("stats.byHour")}
            aside={
              peak !== null && (
                <span className="text-[13px] text-muted-foreground tabular-nums">
                  {t("stats.peakHour", { from: peak, to: (peak + 1) % 24 })}
                </span>
              )
            }
          >
            <HourBars hours={summary.hours} />
          </Panel>
        </div>
      </div>

      <div className="max-w-5xl">
        <Panel title={t("stats.books")}>
          {summary.books.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("stats.booksEmpty")}</p>
          ) : (
            <ul className="flex flex-col">
              {summary.books.map((b, i) => (
                <BookRow key={b.hash ?? `${i}`} book={b} top={summary.books[0]?.duration ?? 1} />
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </>
  );
}

function Calendar() {
  const { t, i18n } = useTranslation();
  const calendar = useQuery(calendarQuery);
  if (!calendar.data) return null;
  return (
    <div className="max-w-5xl">
      <Panel
        title={t("stats.calendar")}
        aside={
          <span className="text-[13px] text-muted-foreground tabular-nums">
            {t("stats.calendarDays", { count: new Intl.NumberFormat(i18n.language).format(calendar.data.days.length) })}
          </span>
        }
      >
        <Heatmap calendar={calendar.data} />
      </Panel>
    </div>
  );
}

function Empty() {
  const { t } = useTranslation();
  return (
    <section className="flex max-w-3xl flex-col items-start gap-3 rounded-xl border border-dashed border-input p-6">
      <div className="flex size-10 items-center justify-center rounded-[10px] bg-muted text-muted-foreground [&_svg]:size-5">
        <ChartNoAxesColumn />
      </div>
      <h2 className="text-base font-semibold">{t("stats.empty.title")}</h2>
      <p className="max-w-130 text-sm text-pretty text-muted-foreground">{t("stats.empty.text")}</p>
      <Link to="/help/statistics" className={`${helpLink} text-sm`}>
        {t("stats.empty.help")}
      </Link>
    </section>
  );
}

export function StatisticsPage() {
  const { t } = useTranslation();
  const { period = "week" } = route.useSearch();
  // Прошлый период виден, пока грузится новый: иначе переключатель и экран на миг
  // сменяются «Загрузкой» — снимка ещё «нет».
  const summary = useQuery({ ...summaryQuery(period), placeholderData: keepPreviousData });
  const hasSnapshot = summary.data?.as_of != null;

  return (
    <>
      <header className="flex max-w-5xl flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("stats.title")}</h1>
          <p className="text-sm text-pretty text-muted-foreground">
            {t("stats.description")}{" "}
            <Link to="/help/statistics" hash="numbers" className={helpLink}>
              {t("stats.howCounted")}
            </Link>
          </p>
        </div>
        {hasSnapshot && (
          <nav className="flex gap-0.5 self-start rounded-lg bg-muted p-[3px]">
            {periods.map((p) => (
              <Link
                key={p}
                to="/statistics"
                search={p === "week" ? {} : { period: p }}
                className={cn(
                  "flex h-[30px] items-center rounded-md px-3 text-[13px] font-medium text-muted-foreground hover:text-foreground",
                  p === period && "bg-background text-foreground shadow-sm",
                )}
              >
                {t(`stats.periods.${p}`)}
              </Link>
            ))}
          </nav>
        )}
      </header>

      {summary.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {summary.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
      {summary.data && !hasSnapshot && <Empty />}
      {summary.data && hasSnapshot && (
        <>
          <Summary summary={summary.data} />
          <Calendar />
        </>
      )}

      <StatisticsCard />
    </>
  );
}
