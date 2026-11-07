import type { BookReadingStats, BookStatistics } from "@ksb/api-client";
import { Link } from "@tanstack/react-router";
import { ChartNoAxesColumn } from "lucide-react";
import { useTranslation } from "react-i18next";
import { HelpTrans, helpLink } from "@/components/HelpParts";
import { SessionList } from "@/components/SessionList";
import { AsOfMark, isStale, StatTile } from "@/components/StatsCharts";
import { formatPercent } from "@/lib/books";
import { formatDate, formatDuration, formatSeconds } from "@/lib/format";

/** «+4,1 % сегодня» — только рост, заметный при одном знаке после запятой. */
export function TodayChange({ value }: { value: number | undefined }) {
  const { t, i18n } = useTranslation();
  if (value === undefined || Math.round(value * 1000) <= 0) return null;
  return (
    <span className="text-[13px] font-medium text-chart-5 tabular-nums">
      {t("book.today", { value: formatPercent(value, i18n.language) })}
    </span>
  );
}

/** «стр. 241 из 386» и «≈ 3 ч 10 мин до конца» — то, что из статистики известно. */
export function useBookFigures(stats: BookReadingStats | null | undefined) {
  const { t, i18n } = useTranslation();
  const nf = new Intl.NumberFormat(i18n.language);
  return {
    page:
      stats?.current_page != null && stats.pages != null
        ? t("book.stats.page", { page: nf.format(stats.current_page), pages: nf.format(stats.pages) })
        : null,
    left:
      stats?.time_left != null && stats.time_left > 0 ? formatDuration(stats.time_left, i18n.language) : null,
  };
}

function Hint({ data }: { data: BookStatistics }) {
  const { t } = useTranslation();
  const noSnapshot = data.as_of === null;
  return (
    <section className="flex max-w-5xl gap-3 rounded-xl border border-dashed border-input p-5 text-sm">
      <ChartNoAxesColumn className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <div className="flex flex-col gap-0.5">
        <span className="font-semibold">{t("book.stats.title")}</span>
        <span className="text-pretty text-muted-foreground">
          {noSnapshot ? t("book.stats.noSnapshot") : <HelpTrans k="book.stats.notInSnapshot" />}{" "}
          {noSnapshot ? (
            <Link to="/help/statistics" className={helpLink}>
              {t("help.more")}
            </Link>
          ) : (
            <Link to="/help/troubleshooting" hash="stats-no-titles" className={helpLink}>
              {t("help.more")}
            </Link>
          )}
        </span>
      </div>
    </section>
  );
}

/** Раздел «Статистика чтения» карточки книги; без статистики книги — подсказка о причине. */
export function BookStatsSection({ hash, data, now }: { hash: string; data: BookStatistics; now: number }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { page, left } = useBookFigures(data.stats);
  if (data.stats === null) return <Hint data={data} />;
  const st = data.stats;
  const nf = new Intl.NumberFormat(lang);
  const from = st.first_read_at && formatDate(st.first_read_at, lang);
  const to = st.last_read_at && formatDate(st.last_read_at, lang);
  const meta = [
    page,
    st.page_time !== null && t("book.stats.pageTime", { value: formatSeconds(st.page_time, lang) }),
    from && to && (from === to ? from : t("book.stats.dates", { from, to })),
  ].filter(Boolean);

  return (
    <section className="flex max-w-5xl flex-col gap-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <h2 className="text-[15px] leading-5 font-semibold">{t("book.stats.title")}</h2>
          {data.as_of && isStale(data.as_of, now) && <AsOfMark asOf={data.as_of} />}
        </div>
        <Link
          to="/help/statistics"
          hash="numbers"
          className="shrink-0 text-[13px] text-muted-foreground hover:text-foreground"
        >
          {t("help.more")}
        </Link>
      </div>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-[repeat(auto-fill,minmax(200px,1fr))]">
        <StatTile label={t("stats.tiles.time")} value={formatDuration(st.duration, lang)} />
        <StatTile
          label={t("book.stats.pagesRead")}
          value={
            <>
              {nf.format(st.pages_read)}
              {st.pages !== null && (
                <span className="text-[15px] font-normal tracking-normal text-muted-foreground">
                  {" "}
                  {t("book.stats.ofPages", { pages: nf.format(st.pages) })}
                </span>
              )}
            </>
          }
        />
        <StatTile label={t("book.stats.days")} value={nf.format(st.days)} />
        {left ? (
          <StatTile label={t("book.stats.left")} value={`≈ ${left}`} />
        ) : (
          <StatTile label={t("stats.tiles.sessions")} value={nf.format(st.sessions)} />
        )}
      </div>
      {meta.length > 0 && <p className="text-[13px] text-muted-foreground tabular-nums">{meta.join(" · ")}</p>}
      <div className="flex flex-col gap-3.5 rounded-xl border bg-card p-5">
        <h3 className="text-[15px] leading-5 font-semibold">{t("home.sessions")}</h3>
        <SessionList hash={hash} limit={10} />
      </div>
    </section>
  );
}
