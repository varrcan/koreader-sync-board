import type { BookFilter } from "@ksb/api-client";
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { Library } from "lucide-react";
import { useTranslation } from "react-i18next";
import { BookRow } from "@/components/BookParts";
import { Button } from "@/components/ui/button";
import { deviceLabel } from "@/lib/books";
import { formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";
import { booksQuery } from "@/progress";

const filters = [undefined, "reading", "finished", "unknown"] as const;

export function BooksPage() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const { filter } = useSearch({ from: "/app/books" });
  // Пока грузится другой фильтр, остаётся прежний список: переключатель со
  // счётчиками не мигает.
  const books = useInfiniteQuery({ ...booksQuery(filter), placeholderData: keepPreviousData });
  const counts = books.data?.pages[0]?.counts;
  const now = books.dataUpdatedAt;
  const items = books.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <>
      <div className="flex max-w-3xl flex-col gap-1.5">
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("books.title")}</h1>
        <p className="text-sm text-pretty text-muted-foreground">{t("books.description")}</p>
        {/* В шапке телефона места нет: библиотека с узкого экрана — отсюда. */}
        <Link
          to="/library"
          className="flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground md:hidden [&_svg]:size-4"
        >
          <Library />
          {t("nav.library")}
        </Link>
      </div>

      <nav className="flex max-w-3xl flex-wrap gap-1 self-start rounded-lg bg-muted p-1" aria-label={t("books.title")}>
        {filters.map((f) => {
          const key: BookFilter | "all" = f ?? "all";
          return (
            <Link
              key={key}
              to="/books"
              search={f ? { filter: f } : {}}
              className={cn(
                "flex h-8 items-center gap-1.5 rounded-md px-3 text-sm text-muted-foreground hover:text-foreground",
                f === filter && "bg-background font-medium text-foreground shadow-sm",
              )}
            >
              {t(`books.filters.${key}`)}
              {counts && <span className="text-xs tabular-nums opacity-70">{counts[key]}</span>}
            </Link>
          );
        })}
      </nav>

      {books.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {books.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
      {books.isSuccess && items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t(`books.empty.${filter ?? "all"}`)}</p>
      )}

      {items.length > 0 && (
        <div className="grid max-w-5xl grid-cols-[repeat(auto-fill,minmax(min(100%,300px),1fr))] gap-3">
          {items.map((b) => (
            <BookRow key={b.hash} book={b} meta={`${deviceLabel(b)} · ${formatRelative(b.updated_at, lang, now)}`} />
          ))}
        </div>
      )}
      {books.hasNextPage && (
        <Button
          variant="outline"
          className="self-start"
          disabled={books.isFetchingNextPage}
          onClick={() => void books.fetchNextPage()}
        >
          {t("common.loadMore")}
        </Button>
      )}
    </>
  );
}
