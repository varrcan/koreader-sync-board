import type { LibraryBook } from "@ksb/api-client";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { BookDown, FolderCog, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/auth";
import { BookCover } from "@/components/BookCover";
import { DropTarget, UploadButton, UploadQueue, useUploadQueue, WebdavBooks } from "@/components/LibraryUpload";
import { ScanStatus } from "@/components/ScanStatus";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { displayTitle } from "@/lib/books";
import { libraryBooksQuery, useLibrarySummary } from "@/library";

const searchDelay = 300;

function LibraryRow({ book }: { book: LibraryBook }) {
  return (
    <Link
      to="/library/books/$id"
      params={{ id: String(book.id) }}
      className="flex min-w-0 items-center gap-3 rounded-xl border py-2.5 pr-3.5 pl-2.5 hover:bg-card"
    >
      <BookCover book={book} size="S" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="line-clamp-1 font-serif text-sm leading-tight font-semibold [overflow-wrap:anywhere]">
          {displayTitle(book)}
        </span>
        {book.authors && <span className="truncate text-xs text-muted-foreground">{book.authors}</span>}
        <div className="flex flex-wrap gap-1">
          {book.formats.map((f) => (
            <Badge key={f} variant="secondary" className="px-1.5 py-0 text-[11px] uppercase">
              {f}
            </Badge>
          ))}
        </div>
      </div>
    </Link>
  );
}

export function LibraryPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  const summary = useLibrarySummary();
  const { q = "" } = useSearch({ from: "/app/library" });
  const navigate = useNavigate({ from: "/library" });
  const isAdmin = me?.role === "admin";
  const upload = useUploadQueue();

  // Поле следует за адресом (назад в браузере), адрес — за полем с задержкой.
  const [text, setText] = useState(q);
  const [shownQ, setShownQ] = useState(q);
  if (q !== shownQ) {
    setShownQ(q);
    setText(q);
  }
  useEffect(() => {
    if (text === q) return;
    const timer = setTimeout(() => void navigate({ search: text ? { q: text } : {}, replace: true }), searchDelay);
    return () => clearTimeout(timer);
  }, [text, q, navigate]);

  const books = useInfiniteQuery({ ...libraryBooksQuery(q), placeholderData: keepPreviousData });
  const items = books.data?.pages.flatMap((p) => p.items) ?? [];
  const empty = summary.data?.files === 0;

  return (
    <>
      <div className="flex max-w-5xl flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("library.title")}</h1>
          {summary.data && (
            <div className="flex flex-col gap-0.5">
              <p className="text-sm text-muted-foreground tabular-nums">
                {t("library.books")}: {summary.data.books} · {t("library.files")}: {summary.data.files}
              </p>
              <ScanStatus scan={summary.data.scan} now={summary.dataUpdatedAt} />
            </div>
          )}
        </div>
        <div className="flex flex-wrap gap-2 self-start">
          <UploadButton onFiles={upload.add} />
          <Button variant="outline" asChild>
            <Link to="/help/opds" hash="setup">
              <BookDown />
              {t("library.opdsLink")}
            </Link>
          </Button>
          {isAdmin && (
            <Button variant="outline" asChild>
              <Link to="/library/roots">
                <FolderCog />
                {t("library.rootsLink")}
              </Link>
            </Button>
          )}
        </div>
      </div>

      <DropTarget onFiles={upload.add} />
      <UploadQueue items={upload.items} busy={upload.busy} onClear={upload.clear} />
      {!empty && (
        <details className="max-w-5xl rounded-xl border px-4 py-3">
          <summary className="cursor-pointer text-sm font-medium select-none">{t("library.upload.webdavTitle")}</summary>
          <WebdavBooks className="mt-3" />
        </details>
      )}

      {!empty && (
        <div className="relative max-w-5xl">
          <Search className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            className="pl-8.5"
            maxLength={200}
            placeholder={t("library.search")}
            aria-label={t("library.search")}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        </div>
      )}

      {books.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {books.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
      {books.isSuccess && items.length === 0 && empty && (
        <Card className="max-w-3xl gap-4 px-6">
          <div className="flex flex-col gap-1.5">
            <h2 className="font-serif text-xl font-medium">{t("library.empty")}</h2>
            <p className="text-sm text-pretty text-muted-foreground">{t("library.emptyUpload")}</p>
            <p className="text-sm text-pretty text-muted-foreground">
              {t(isAdmin ? "library.emptyAdmin" : "library.emptyUser")}
            </p>
          </div>
          <WebdavBooks />
        </Card>
      )}
      {books.isSuccess && items.length === 0 && !empty && (
        <p className="text-sm text-pretty text-muted-foreground">{q ? t("library.noResults") : t("library.empty")}</p>
      )}

      {items.length > 0 && (
        <div className="grid max-w-5xl grid-cols-[repeat(auto-fill,minmax(min(100%,300px),1fr))] gap-3">
          {items.map((b) => (
            <LibraryRow key={b.id} book={b} />
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
