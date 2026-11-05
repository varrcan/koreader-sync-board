import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ArrowLeft, CircleHelp, TabletSmartphone } from "lucide-react";
import { useTranslation } from "react-i18next";
import { ApiFailure } from "@/api";
import { ActivityFeed } from "@/components/ActivityFeed";
import { BookCover } from "@/components/BookCover";
import { BookName, ProgressBar } from "@/components/BookParts";
import { MenuLabel, Value } from "@/components/KoreaderSetup";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { deviceLabel, formatPercent, isUnknown } from "@/lib/books";
import { formatRelative } from "@/lib/format";
import { bookQuery } from "@/progress";

function BackLink() {
  const { t } = useTranslation();
  return (
    <Link
      to="/books"
      className="flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground [&_svg]:size-4"
    >
      <ArrowLeft />
      {t("book.back")}
    </Link>
  );
}

export function BookPage() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { hash } = useParams({ from: "/app/books/$hash" });
  const detail = useQuery(bookQuery(hash));
  const now = detail.dataUpdatedAt;

  if (detail.isPending) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  if (detail.isError) {
    const notFound = detail.error instanceof ApiFailure && detail.error.code === "not_found";
    return (
      <>
        <BackLink />
        {notFound ? (
          <Card className="max-w-3xl items-start gap-2 px-6">
            <h1 className="font-serif text-2xl font-medium">{t("book.notFound")}</h1>
            <p className="text-sm text-muted-foreground">{t("book.notFoundText")}</p>
          </Card>
        ) : (
          <p className="text-sm text-destructive">{t("common.loadError")}</p>
        )}
      </>
    );
  }

  const { book, devices } = detail.data;
  const unknown = isUnknown(book);
  return (
    <>
      <BackLink />
      <section className="flex max-w-5xl flex-wrap gap-[clamp(16px,3vw,32px)]">
        <BookCover book={book} size="L" />
        <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-1.5">
          {book.finished && (
            <Badge variant="secondary" className="self-start">
              {t("books.finished")}
            </Badge>
          )}
          <h1>
            <BookName book={book} className="text-[clamp(24px,4vw,32px)] leading-tight font-medium text-balance" />
          </h1>
          {book.authors && <div className="text-base text-muted-foreground">{book.authors}</div>}
          {book.filename && (
            <div className="truncate text-sm text-muted-foreground">
              {t("book.file")}: <span className="font-mono text-[13px]">{book.filename}</span>
            </div>
          )}
          <div className="mt-auto flex flex-col gap-2 pt-4">
            <span className="text-[clamp(32px,3.6vw,40px)] leading-none font-semibold tracking-[-0.02em] tabular-nums">
              {formatPercent(book.percentage, lang)}
            </span>
            <ProgressBar value={book.percentage} />
            <span className="flex items-center gap-1.5 text-[13px] text-muted-foreground [&_svg]:size-3.5">
              <TabletSmartphone />
              {t("book.lastSync", { device: deviceLabel(book), when: formatRelative(book.updated_at, lang, now) })}
            </span>
          </div>
        </div>
      </section>

      {unknown && (
        <div className="flex max-w-3xl gap-2.5 rounded-lg border border-chart-2/45 bg-chart-2/12 px-3 py-2.5 text-sm">
          <CircleHelp className="mt-0.5 size-4 shrink-0 text-chart-2" />
          <div className="flex flex-col gap-0.5">
            <span className="font-semibold">
              {t("book.unknownTitle")} <MenuLabel item="sendMetadata" />
            </span>
            <span className="text-pretty text-muted-foreground">{t("book.unknownText")}</span>
          </div>
        </div>
      )}

      <div className="max-w-3xl">
        <Value label={t("book.hash")} value={book.hash} />
      </div>

      <div className="flex max-w-5xl flex-wrap items-start gap-4">
        <section className="flex min-w-0 flex-[1_1_300px] flex-col gap-2.5 rounded-xl border bg-card p-5">
          <h2 className="text-[15px] leading-5 font-semibold">{t("book.devices")}</h2>
          <ul className="flex flex-col">
            {devices.map((d) => (
              <li
                key={`${d.device_name}-${d.updated_at}`}
                className="flex items-center gap-3 border-t py-2 text-[13px] leading-[18px]"
              >
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="truncate font-medium">{deviceLabel(d)}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {d.device_name !== null && `${d.reported_device} · `}
                    {formatRelative(d.updated_at, lang, now)}
                  </span>
                </div>
                <span className="font-medium tabular-nums">{formatPercent(d.percentage, lang)}</span>
              </li>
            ))}
          </ul>
        </section>
        <section className="flex min-w-0 flex-[2_1_380px] flex-col gap-2.5 rounded-xl border bg-card p-5">
          <h2 className="text-[15px] leading-5 font-semibold">{t("book.history")}</h2>
          <ActivityFeed hash={book.hash} />
        </section>
      </div>
    </>
  );
}
