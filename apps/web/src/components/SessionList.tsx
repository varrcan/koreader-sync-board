import type { ReadingSession } from "@ksb/api-client";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { formatDay, formatTime, groupByDay, statBookTitle } from "@/lib/books";
import { formatDuration } from "@/lib/format";
import { sessionsQuery } from "@/statistics";

/**
 * Сессии чтения по дням. Без hash — первая страница всех сессий с названиями книг (главная);
 * с hash — сессии одной книги без названия и с подгрузкой. Элементы отдаются без обёртки:
 * отступы задаёт карточка, в которой стоит список.
 */
export function SessionList({ hash, limit }: { hash?: string; limit?: number }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const sessions = useInfiniteQuery(sessionsQuery(limit, hash));
  const now = sessions.dataUpdatedAt;
  const items = sessions.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <>
      {sessions.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {sessions.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
      {sessions.isSuccess && items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("home.sessionsEmpty")}</p>
      )}
      {groupByDay(items, now, (s: ReadingSession) => s.started_at).map((d) => (
        <div key={d.day.getTime()} className="flex flex-col">
          <h3 className="flex justify-between gap-2 pb-1.5 text-xs font-medium tracking-[.06em] text-muted-foreground uppercase">
            <span>
              {d.ago === 0 ? t("feed.today") : d.ago === 1 ? t("feed.yesterday") : formatDay(d.day, lang, now)}
            </span>
            <span className="tracking-normal normal-case tabular-nums">
              {formatDuration(
                d.items.reduce((sum, s) => sum + s.duration, 0),
                lang,
              )}
            </span>
          </h3>
          <ul className="flex flex-col">
            {d.items.map((s) => (
              <li key={`${s.started_at}-${s.book.hash}`} className="flex gap-3 border-t py-[7px]">
                <span className="w-24 shrink-0 text-[13px] leading-[18px] text-muted-foreground tabular-nums">
                  {formatTime(s.started_at, lang)}–{formatTime(s.ended_at, lang)}
                </span>
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <div className="flex items-baseline gap-2 text-sm leading-[18px] tabular-nums">
                    <span className="font-semibold">{formatDuration(s.duration, lang)}</span>
                    <span className="text-[13px] text-muted-foreground">{t("stats.pages", { count: s.pages })}</span>
                  </div>
                  {!hash && (
                    <div className="truncate text-xs text-muted-foreground">
                      {statBookTitle(s.book, t("books.unknown"))}
                    </div>
                  )}
                </div>
              </li>
            ))}
          </ul>
        </div>
      ))}
      {hash && sessions.hasNextPage && (
        <Button
          variant="outline"
          size="sm"
          className="self-start"
          disabled={sessions.isFetchingNextPage}
          onClick={() => void sessions.fetchNextPage()}
        >
          {t("common.loadMore")}
        </Button>
      )}
    </>
  );
}
