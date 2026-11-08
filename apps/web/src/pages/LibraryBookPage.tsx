import type { LibraryBook, LibraryFile } from "@ksb/api-client";
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowLeft, Download, Merge, Pin, Split } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiFailure } from "@/api";
import { meQuery } from "@/auth";
import { BookCover } from "@/components/BookCover";
import { FormError } from "@/components/FormError";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { displayTitle, formatPercent } from "@/lib/books";
import { formatSize } from "@/lib/format";
import {
  downloadFile,
  libraryBookQuery,
  libraryBooksQuery,
  libraryErrorText,
  useMergeBooks,
  useSplitFile,
} from "@/library";

const searchDelay = 300;

function BackLink() {
  const { t } = useTranslation();
  return (
    <Link
      to="/library"
      className="flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground [&_svg]:size-4"
    >
      <ArrowLeft />
      {t("library.title")}
    </Link>
  );
}

function NotFound() {
  const { t } = useTranslation();
  return (
    <>
      <BackLink />
      <Card className="max-w-3xl items-start gap-2 px-6">
        <h1 className="font-serif text-2xl font-medium">{t("library.detail.notFound")}</h1>
        <p className="text-sm text-muted-foreground">{t("library.detail.notFoundText")}</p>
      </Card>
    </>
  );
}

function MergeDialog({ book, onClose }: { book: LibraryBook; onClose: () => void }) {
  const { t } = useTranslation();
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");
  const [chosen, setChosen] = useState<LibraryBook | null>(null);
  const merge = useMergeBooks();
  useEffect(() => {
    const timer = setTimeout(() => setQuery(text), searchDelay);
    return () => clearTimeout(timer);
  }, [text]);
  const found = useInfiniteQuery({ ...libraryBooksQuery(query, 20), placeholderData: keepPreviousData });
  const items = found.data?.pages[0]?.items.filter((b) => b.id !== book.id) ?? [];

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="grid-cols-[minmax(0,1fr)]">
        <DialogHeader>
          <DialogTitle>{t("library.detail.mergeTitle", { title: displayTitle(book) })}</DialogTitle>
          <DialogDescription>
            {chosen
              ? t("library.detail.mergeConfirmText", { from: displayTitle(chosen), into: displayTitle(book) })
              : t("library.detail.mergeDescription")}
          </DialogDescription>
        </DialogHeader>
        {!chosen && (
          <>
            <Input
              type="search"
              autoFocus
              maxLength={200}
              placeholder={t("library.search")}
              aria-label={t("library.search")}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
            {found.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
            {found.isSuccess && items.length === 0 && (
              <p className="text-sm text-muted-foreground">{t("library.noResults")}</p>
            )}
            <ul className="flex max-h-72 flex-col gap-1 overflow-y-auto">
              {items.map((b) => (
                <li key={b.id}>
                  <button
                    type="button"
                    className="flex w-full min-w-0 flex-col gap-0.5 rounded-lg px-2.5 py-2 text-left hover:bg-accent"
                    onClick={() => setChosen(b)}
                  >
                    <span className="truncate text-sm font-medium">{displayTitle(b)}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {[b.authors, b.formats.join(", ")].filter(Boolean).join(" · ")}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </>
        )}
        {chosen && (
          <>
            <FormError message={merge.isError ? libraryErrorText(t, merge.error) : undefined} />
            <DialogFooter>
              <Button variant="outline" onClick={() => setChosen(null)}>
                {t("library.detail.mergeBack")}
              </Button>
              <Button
                disabled={merge.isPending}
                onClick={() => merge.mutate({ into: book.id, from: chosen.id }, { onSuccess: onClose })}
              >
                {t("library.detail.mergeConfirm")}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

function FileRow({
  file,
  canSplit,
  splitting,
  onSplit,
}: {
  file: LibraryFile;
  canSplit: boolean;
  splitting: boolean;
  onSplit: () => void;
}) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [unavailable, setUnavailable] = useState(false);
  const [checking, setChecking] = useState(false);
  const download = async () => {
    setChecking(true);
    setUnavailable(!(await downloadFile(file.id)));
    setChecking(false);
  };
  return (
    <li className="flex flex-col gap-2 border-t py-3 first:border-t-0">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <Badge variant="secondary" className="uppercase">
          {file.format}
        </Badge>
        <span className="text-muted-foreground tabular-nums">{formatSize(file.size, lang)}</span>
        {file.pinned && (
          <span
            className="flex items-center gap-1 text-xs text-muted-foreground [&_svg]:size-3.5"
            title={t("library.detail.pinnedHint")}
          >
            <Pin />
            {t("library.detail.pinned")}
          </span>
        )}
        {file.progress && (
          <Link
            to="/books/$hash"
            params={{ hash: file.progress.hash }}
            className="text-xs font-medium tabular-nums underline-offset-2 hover:underline"
          >
            {t("library.detail.yourProgress", { percent: formatPercent(file.progress.percentage, lang) })}
          </Link>
        )}
      </div>
      <p className="font-mono text-xs break-all text-muted-foreground">
        {file.root_name} / {file.path}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" disabled={checking} onClick={() => void download()}>
          <Download />
          {t("library.detail.download")}
        </Button>
        {canSplit && (
          <Button variant="ghost" size="sm" disabled={splitting} onClick={onSplit}>
            <Split />
            {t("library.detail.split")}
          </Button>
        )}
        {unavailable && <span className="text-sm text-destructive">{t("errors.file_unavailable")}</span>}
      </div>
    </li>
  );
}

function BookView({ id }: { id: number }) {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  const detail = useQuery(libraryBookQuery(id));
  const split = useSplitFile();
  const navigate = useNavigate();
  const [merging, setMerging] = useState(false);
  const isAdmin = me?.role === "admin";

  if (detail.isPending) return <p className="text-sm text-muted-foreground">{t("common.loading")}</p>;
  if (detail.isError) {
    if (detail.error instanceof ApiFailure && detail.error.code === "not_found") return <NotFound />;
    return (
      <>
        <BackLink />
        <p className="text-sm text-destructive">{t("common.loadError")}</p>
      </>
    );
  }

  const { book, files } = detail.data;
  const series = book.series && (book.series_index === null ? book.series : `${book.series} #${book.series_index}`);
  const facts = [
    series && { label: t("library.detail.series"), value: series },
    book.language && { label: t("library.detail.language"), value: book.language },
  ].filter((f) => !!f);

  return (
    <>
      <BackLink />
      <section className="flex max-w-5xl flex-wrap gap-[clamp(16px,3vw,32px)]">
        <BookCover book={book} size="L" />
        <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-1.5">
          <h1 className="font-serif text-[clamp(24px,4vw,32px)] leading-tight font-semibold text-balance [overflow-wrap:anywhere]">
            {displayTitle(book)}
          </h1>
          {book.authors && <div className="text-base text-muted-foreground">{book.authors}</div>}
          {facts.map((f) => (
            <div key={f.label} className="text-sm text-muted-foreground">
              {f.label}: <span className="text-foreground">{f.value}</span>
            </div>
          ))}
          {isAdmin && (
            <Button variant="outline" className="mt-3 self-start" onClick={() => setMerging(true)}>
              <Merge />
              {t("library.detail.merge")}
            </Button>
          )}
        </div>
      </section>

      <section className="flex max-w-5xl flex-col gap-1 rounded-xl border bg-card p-5">
        <h2 className="text-[15px] leading-5 font-semibold">{t("library.detail.files")}</h2>
        <FormError message={split.isError ? libraryErrorText(t, split.error) : undefined} />
        <ul className="flex flex-col">
          {files.map((f) => (
            <FileRow
              key={f.id}
              file={f}
              canSplit={isAdmin && files.length > 1}
              splitting={split.isPending}
              onSplit={() =>
                split.mutate(f.id, {
                  onSuccess: (created) =>
                    void navigate({ to: "/library/books/$id", params: { id: String(created.book.id) } }),
                })
              }
            />
          ))}
        </ul>
      </section>
      {merging && <MergeDialog book={book} onClose={() => setMerging(false)} />}
    </>
  );
}

export function LibraryBookPage() {
  const params = useParams({ from: "/app/library/books/$id" });
  const id = Number(params.id);
  if (!Number.isSafeInteger(id) || id <= 0) return <NotFound />;
  return <BookView key={id} id={id} />;
}
