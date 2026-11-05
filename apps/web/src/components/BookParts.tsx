import type { Book } from "@ksb/api-client";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { BookCover } from "@/components/BookCover";
import { displayTitle, formatPercent, isUnknown, shortHash } from "@/lib/books";
import { cn } from "@/lib/utils";

export function ProgressBar({ value, muted, className }: { value: number; muted?: boolean; className?: string }) {
  return (
    <div className={cn("h-1.5 overflow-hidden rounded-full bg-muted", className)}>
      <div
        className={cn("h-full rounded-full", muted ? "bg-muted-foreground" : "bg-primary")}
        style={{ width: `${Math.min(Math.max(value, 0), 1) * 100}%` }}
      />
    </div>
  );
}

/** Название книги: у неизвестной — «Неизвестная книга» обычным шрифтом и приглушённо. */
export function BookName({ book, className }: { book: Book; className?: string }) {
  const { t } = useTranslation();
  const unknown = isUnknown(book);
  return (
    <span
      className={cn(
        "font-semibold [overflow-wrap:anywhere] hyphens-auto",
        unknown ? "font-sans text-muted-foreground" : "font-serif",
        className,
      )}
    >
      {displayTitle(book) ?? t("books.unknown")}
    </span>
  );
}

/** Строка книги: обложка S, название, авторы или хеш, устройство и время, процент. */
export function BookRow({ book, meta }: { book: Book; meta: string }) {
  const { i18n } = useTranslation();
  const unknown = isUnknown(book);
  return (
    <Link
      to="/books/$hash"
      params={{ hash: book.hash }}
      className="flex min-w-0 items-center gap-3 rounded-xl border py-2.5 pr-3.5 pl-2.5 hover:bg-card"
    >
      <BookCover book={book} size="S" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <BookName book={book} className="line-clamp-1 text-sm leading-tight" />
        {(unknown || book.authors) && (
          <span className={cn("truncate text-xs text-muted-foreground", unknown && "font-mono")}>
            {unknown ? shortHash(book.hash) : book.authors}
          </span>
        )}
        <span className="truncate text-xs text-muted-foreground">{meta}</span>
        <div className="mt-0.5 flex items-center gap-2">
          <ProgressBar value={book.percentage} muted className="h-[3px] flex-1" />
          <span className="text-xs font-medium text-muted-foreground tabular-nums">
            {formatPercent(book.percentage, i18n.language)}
          </span>
        </div>
      </div>
    </Link>
  );
}
