import type { StatisticsSummary } from "@ksb/api-client";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChartNoAxesColumn } from "lucide-react";
import { useTranslation } from "react-i18next";
import { helpLink } from "@/components/HelpParts";
import {
  AsOfMark,
  ChangeText,
  DayBars,
  isStale,
  NoDataHint,
  speed,
  useChange,
} from "@/components/StatsCharts";
import { SessionList } from "@/components/SessionList";
import { formatDuration } from "@/lib/format";
import { summaryQuery } from "@/statistics";

const card = "flex min-w-0 flex-col rounded-xl border bg-card p-5";

function Week({ summary }: { summary: StatisticsSummary }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { totals, previous } = summary;
  const change = useChange(summary.series, "week")(totals.duration, previous.duration);
  const nf = new Intl.NumberFormat(lang);
  const cur = speed(totals);
  const stale = summary.as_of !== null && isStale(summary.as_of, summary.to);
  const figures = [
    [t("stats.tiles.pages"), nf.format(totals.pages)],
    [t("stats.tiles.sessions"), nf.format(totals.sessions)],
    [t("stats.tiles.speed"), cur === null ? "—" : t("stats.speed", { value: nf.format(cur) })],
  ];
  return (
    <section className={`${card} flex-[2_1_420px] gap-4`}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <h2 className="text-[15px] leading-5 font-semibold">{t("home.week")}</h2>
            {stale && summary.as_of && <AsOfMark asOf={summary.as_of} />}
          </div>
          <div className="text-[28px] leading-8 font-semibold tracking-[-0.01em] tabular-nums">
            {formatDuration(totals.duration, lang)}
          </div>
          <ChangeText change={change} />
        </div>
        <dl className="flex gap-5 text-[13px] text-muted-foreground tabular-nums">
          {figures.map(([label, value]) => (
            <div key={label} className="flex flex-col-reverse gap-0.5">
              <dd className="text-[15px] font-semibold text-foreground">{value}</dd>
              <dt>{label}</dt>
            </div>
          ))}
        </dl>
      </div>
      <DayBars series={summary.series} monthly={false} values className="flex-1" />
      {summary.series.some((b) => b.no_data) && <NoDataHint />}
    </section>
  );
}

function Sessions({ stale }: { stale: string | null }) {
  const { t } = useTranslation();
  return (
    <section className={`${card} flex-[1_1_320px] gap-3.5`}>
      <div className="flex items-baseline justify-between gap-3">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <h2 className="text-[15px] leading-5 font-semibold">{t("home.sessions")}</h2>
          {stale && <AsOfMark asOf={stale} />}
        </div>
        <Link to="/statistics" className="shrink-0 text-[13px] text-muted-foreground hover:text-foreground">
          {t("home.toStats")}
        </Link>
      </div>
      <SessionList limit={5} />
    </section>
  );
}

function NoStatistics() {
  const { t } = useTranslation();
  return (
    <section className="flex max-w-5xl flex-col items-start gap-3 rounded-xl border border-dashed border-input p-6">
      <div className="flex size-10 items-center justify-center rounded-[10px] bg-muted text-muted-foreground [&_svg]:size-5">
        <ChartNoAxesColumn />
      </div>
      <h2 className="text-base font-semibold">{t("home.noStats.title")}</h2>
      <p className="max-w-130 text-sm text-pretty text-muted-foreground">{t("home.noStats.text")}</p>
      <Link to="/help/statistics" className={`${helpLink} text-sm`}>
        {t("help.more")}
      </Link>
    </section>
  );
}

/**
 * Неделя и сессии на главной. Без снимка: при книгах — карточка «Статистики пока нет»,
 * без книг — ничего (главная остаётся пустым состоянием).
 */
export function HomeStats({ hasBooks }: { hasBooks: boolean }) {
  const summary = useQuery(summaryQuery("week"));
  if (!summary.data) return null;
  const { as_of: asOf, to } = summary.data;
  if (asOf === null) return hasBooks ? <NoStatistics /> : null;
  return (
    <div className="flex max-w-5xl flex-wrap items-stretch gap-4">
      <Week summary={summary.data} />
      <Sessions stale={isStale(asOf, to) ? asOf : null} />
    </div>
  );
}
