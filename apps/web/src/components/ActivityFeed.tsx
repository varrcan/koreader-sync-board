import type { ActivityItem } from "@ksb/api-client";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  deviceLabel,
  displayTitle,
  formatDay,
  formatPercent,
  formatTime,
  groupByDay,
  isUnknown,
  shortHash,
} from "@/lib/books";
import { cn } from "@/lib/utils";
import { activityQuery } from "@/progress";

function Row({ item, showBook }: { item: ActivityItem; showBook: boolean }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const from = formatPercent(item.from_percentage, lang);
  const to = formatPercent(item.to_percentage, lang);
  const start = formatTime(item.started_at, lang);
  const end = formatTime(item.ended_at, lang);
  const unknown = isUnknown(item);
  const content: ReactNode = (
    <>
      <span className="w-24 shrink-0 text-muted-foreground tabular-nums">
        {start === end ? end : `${start}–${end}`}
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        {showBook && (
          <span className={cn("truncate font-medium", unknown && "text-muted-foreground")}>
            {displayTitle(item) ?? `${t("books.unknown")} · ${shortHash(item.hash)}`}
          </span>
        )}
        <span className={cn("truncate tabular-nums", showBook && "text-xs text-muted-foreground")}>
          {deviceLabel(item)} · {from === to ? to : `${from} → ${to}`}
        </span>
      </span>
    </>
  );
  const row = "flex gap-3 border-t py-2 text-[13px] leading-[18px]";
  const title = item.syncs > 1 ? t("feed.syncs", { count: item.syncs }) : undefined;
  if (!showBook) {
    return (
      <li className={row} title={title}>
        {content}
      </li>
    );
  }
  return (
    <li className="contents">
      <Link
        to="/books/$hash"
        params={{ hash: item.hash }}
        title={title}
        className={cn(row, "-mx-2 rounded-md px-2 hover:bg-accent")}
      >
        {content}
      </Link>
    </li>
  );
}

/** Лента синхронизаций по дням; с hash — лента одной книги без названия в строках. */
export function ActivityFeed({ hash, limit }: { hash?: string; limit?: number }) {
  const { t, i18n } = useTranslation();
  const feed = useInfiniteQuery(activityQuery(hash, limit));
  // Момент загрузки — точка отсчёта для «сегодня/вчера», чтобы рендер оставался чистым.
  const now = feed.dataUpdatedAt;

  if (feed.isPending) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  if (feed.isError) return <p className="text-sm text-destructive">{t("common.loadError")}</p>;
  const items = feed.data.pages.flatMap((p) => p.items);
  if (items.length === 0) return <p className="text-sm text-muted-foreground">{t("feed.empty")}</p>;

  return (
    <div className="flex flex-col gap-4">
      {groupByDay(items, now).map((d) => (
        <section key={d.day.getTime()} className="flex flex-col">
          <h3 className="pb-1.5 text-xs font-medium tracking-[.06em] text-muted-foreground uppercase">
            {d.ago === 0 ? t("feed.today") : d.ago === 1 ? t("feed.yesterday") : formatDay(d.day, i18n.language, now)}
          </h3>
          <ul className="flex flex-col">
            {d.items.map((item) => (
              <Row
                key={`${item.hash}-${item.started_at}-${item.ended_at}-${item.device_name}`}
                item={item}
                showBook={!hash}
              />
            ))}
          </ul>
        </section>
      ))}
      {feed.hasNextPage && (
        <Button
          variant="outline"
          size="sm"
          className="self-start"
          disabled={feed.isFetchingNextPage}
          onClick={() => void feed.fetchNextPage()}
        >
          {t("common.loadMore")}
        </Button>
      )}
    </div>
  );
}
