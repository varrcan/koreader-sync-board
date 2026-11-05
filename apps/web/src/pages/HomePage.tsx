import type { Book } from "@ksb/api-client";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BookOpen, Check, Plus, TabletSmartphone, TriangleAlert } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/auth";
import { ActivityFeed } from "@/components/ActivityFeed";
import { BookCover } from "@/components/BookCover";
import { BookName, BookRow, ProgressBar } from "@/components/BookParts";
import { devicesQuery } from "@/components/DeviceDialogs";
import { MenuLabel, Value } from "@/components/KoreaderSetup";
import { Button } from "@/components/ui/button";
import { deviceLabel, formatPercent } from "@/lib/books";
import { formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";
import { booksQuery } from "@/progress";

const sectionLabel = "text-xs leading-4 font-medium tracking-[.06em] text-muted-foreground uppercase";

function Hero({ book, now }: { book: Book; now: number }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  return (
    <Link
      to="/books/$hash"
      params={{ hash: book.hash }}
      className="flex flex-wrap gap-[clamp(16px,2.4vw,28px)] rounded-[14px] border bg-card p-[clamp(16px,2vw,24px)]"
    >
      <BookCover book={book} size="M" />
      <div className="flex min-w-0 flex-[1_1_280px] flex-col gap-1">
        <div className={sectionLabel}>{t("home.reading")}</div>
        <BookName book={book} className="text-[clamp(22px,2.6vw,28px)] leading-tight font-medium text-balance" />
        {book.authors && <div className="text-[15px] text-muted-foreground">{book.authors}</div>}
        <div className="mt-auto flex flex-col gap-2 pt-3.5">
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
    </Link>
  );
}

function Step({ n, done, children }: { n: number; done?: boolean; children: ReactNode }) {
  return (
    <li className="flex flex-wrap gap-4 border-b px-5 py-4.5 last:border-b-0">
      <div
        className={cn(
          "flex size-7 shrink-0 items-center justify-center rounded-full text-[13px] font-semibold [&_svg]:size-4",
          done || n === 1 ? "bg-primary text-primary-foreground" : "bg-secondary",
        )}
      >
        {done ? <Check /> : n}
      </div>
      {children}
    </li>
  );
}

function EmptyHome({ username }: { username: string }) {
  const { t } = useTranslation();
  const devices = useQuery(devicesQuery);
  // Администратору список отдаёт устройства всех пользователей: чужое его шаг не отмечает.
  const hasDevice = devices.data?.some((d) => d.owner === username && d.revoked_at === null) ?? false;
  return (
    <section className="flex max-w-180 flex-col gap-7 pt-[clamp(0px,3vw,32px)]">
      <div className="flex flex-col items-start gap-3">
        <div className="flex size-11 items-center justify-center rounded-xl bg-muted text-primary [&_svg]:size-5.5">
          <BookOpen />
        </div>
        <h2 className="font-serif text-[clamp(24px,3.4vw,32px)] leading-tight font-medium text-balance">
          {t("home.empty.title")}
        </h2>
        <p className="max-w-140 text-[15px] leading-[22px] text-pretty text-muted-foreground">{t("home.empty.text")}</p>
      </div>
      <ol className="flex flex-col overflow-hidden rounded-xl border bg-card">
        <Step n={1} done={hasDevice}>
          <div className="flex flex-[1_1_220px] flex-col gap-1">
            <div className="text-[15px] font-semibold">{t("home.empty.step1Title")}</div>
            <div className="text-sm text-muted-foreground">
              {hasDevice ? t("home.empty.step1Done") : t("home.empty.step1Text")}
            </div>
          </div>
          <Button asChild variant={hasDevice ? "outline" : "default"} className="self-center">
            <Link to="/devices">
              {hasDevice ? <TabletSmartphone /> : <Plus />}
              {hasDevice ? t("home.empty.openDevices") : t("home.empty.addDevice")}
            </Link>
          </Button>
        </Step>
        <Step n={2}>
          <div className="flex min-w-0 flex-1 flex-col gap-2 text-sm text-muted-foreground">
            <div className="text-[15px] font-semibold text-foreground">{t("home.empty.step2Title")}</div>
            <p>
              {t("home.empty.step2Path")} <MenuLabel item="progressSync" /> → <MenuLabel item="customServer" />{" "}
              {t("home.empty.step2Address")}
            </p>
            <Value value={window.location.origin} />
            <p className="text-pretty">
              {t("home.empty.step2Login")} <MenuLabel item="registerLogin" />: {t("home.empty.step2User")}{" "}
              <code className="font-mono text-foreground">{username}</code> {t("home.empty.step2Password")}
            </p>
            <div className="flex gap-2.5 rounded-lg border border-chart-2/45 bg-chart-2/12 px-3 py-2.5">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-chart-2" />
              <div className="flex flex-col gap-0.5">
                <span className="font-semibold text-foreground">
                  {t("home.empty.metadataTitle")} <MenuLabel item="sendMetadata" />
                </span>
                <span className="text-pretty">{t("home.empty.metadataText")}</span>
              </div>
            </div>
          </div>
        </Step>
        <Step n={3}>
          <div className="flex flex-1 flex-col gap-1">
            <div className="text-[15px] font-semibold">{t("home.empty.step3Title")}</div>
            <div className="text-sm text-muted-foreground">{t("home.empty.step3Text")}</div>
          </div>
        </Step>
      </ol>
    </section>
  );
}

export function HomePage() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { data: me } = useQuery(meQuery);
  // Герой и до трёх «Начато раньше»; счётчик all того же ответа решает, пуста ли полка.
  const reading = useInfiniteQuery(booksQuery("reading", 4));
  const [today] = useState(() => Date.now());
  const now = reading.dataUpdatedAt;
  const date = new Intl.DateTimeFormat(lang, { weekday: "long", day: "numeric", month: "long" }).format(today);

  const page = reading.data?.pages[0];
  const [hero, ...earlier] = page?.items ?? [];

  return (
    <>
      <header className="flex flex-col gap-0.5">
        <div className="text-[13px] text-muted-foreground first-letter:uppercase">{date}</div>
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("nav.home")}</h1>
      </header>

      {reading.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {reading.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}

      {page && page.counts.all === 0 && me && <EmptyHome username={me.username} />}

      {page && page.counts.all > 0 && (
        <>
          {hero && (
            <section className="flex max-w-5xl flex-col gap-3">
              <Hero book={hero} now={now} />
              {earlier.length > 0 && (
                <div className="flex flex-col gap-2">
                  <div className="flex items-baseline justify-between gap-3">
                    <div className={sectionLabel}>{t("home.earlier")}</div>
                    <Link
                      to="/books"
                      search={{ filter: "reading" }}
                      className="text-[13px] text-muted-foreground hover:text-foreground"
                    >
                      {t("home.allBooks")}
                    </Link>
                  </div>
                  <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,300px),1fr))] gap-3">
                    {earlier.map((b) => (
                      <BookRow
                        key={b.hash}
                        book={b}
                        meta={`${deviceLabel(b)} · ${formatRelative(b.updated_at, lang, now)}`}
                      />
                    ))}
                  </div>
                </div>
              )}
            </section>
          )}
          <section className="flex max-w-5xl flex-col gap-2.5 rounded-xl border bg-card p-5">
            <h2 className="text-[15px] leading-5 font-semibold">{t("home.syncs")}</h2>
            <ActivityFeed limit={10} />
          </section>
        </>
      )}
    </>
  );
}
