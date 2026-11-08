import type { LibraryBookDetail, LibraryMetadataCandidate, LibraryMetadataForm } from "@ksb/api-client";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useTranslation } from "react-i18next";
import { ApiFailure } from "@/api";
import { FormError } from "@/components/FormError";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { displayTitle } from "@/lib/books";
import {
  type CandidateSearch,
  libraryCandidatesQuery,
  libraryErrorText,
  providerName,
  useClearMetadata,
  useSaveMetadata,
} from "@/library";

// Адрес внешний: Referer панели каталогу незачем.
function CandidateCover({ url }: { url: string | null }) {
  const [failed, setFailed] = useState(false);
  if (!url || failed) return <div className="h-16 w-11 shrink-0 rounded-sm bg-muted" />;
  return (
    <img
      src={url}
      alt=""
      loading="lazy"
      referrerPolicy="no-referrer"
      className="h-16 w-11 shrink-0 rounded-sm object-cover"
      onError={() => setFailed(true)}
    />
  );
}

function CandidateList({
  bookId,
  onPick,
  footer,
}: {
  bookId: number;
  onPick: (c: LibraryMetadataCandidate | null) => void;
  footer: React.ReactNode;
}) {
  const { t } = useTranslation();
  const [search, setSearch] = useState<CandidateSearch>({});
  // null — поле не трогали: показываем запрос, который построил сервер.
  const [text, setText] = useState<string | null>(null);
  const [isbn, setIsbn] = useState<string | null>(null);
  const found = useQuery({ ...libraryCandidatesQuery(bookId, search), placeholderData: keepPreviousData });
  const shownText = text ?? found.data?.query ?? "";
  const shownIsbn = isbn ?? found.data?.isbn ?? "";
  const failed = found.data?.providers.filter((p) => p.status === "error").map((p) => providerName(t, p.id)) ?? [];
  const canSearch = shownText.trim() !== "" || shownIsbn.trim() !== "";

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSearch) return;
    // Поля фиксируются: при ошибке запроса серверных значений для показа больше нет.
    setText(shownText);
    setIsbn(shownIsbn);
    const next = { q: shownText.trim() || undefined, isbn: shownIsbn.trim() || undefined };
    // Тот же ключ сам не перезапустится, а повтор нужен: каталог мог не ответить.
    if (next.q === search.q && next.isbn === search.isbn) void found.refetch();
    else setSearch(next);
  };

  return (
    <>
      <form className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_10rem_auto]" onSubmit={submit}>
        <Input
          aria-label={t("library.metadata.query")}
          placeholder={t("library.metadata.query")}
          maxLength={200}
          value={shownText}
          onChange={(e) => setText(e.target.value)}
        />
        <Input
          aria-label="ISBN"
          placeholder="ISBN"
          value={shownIsbn}
          onChange={(e) => setIsbn(e.target.value)}
        />
        <Button type="submit" variant="outline" disabled={!canSearch || found.isFetching}>
          {t("library.metadata.search")}
        </Button>
      </form>
      {found.isFetching && <p className="text-sm text-muted-foreground">{t("library.metadata.searching")}</p>}
      {found.isError && (
        <p role="alert" className="text-sm text-destructive">
          {found.error instanceof ApiFailure && found.error.code === "bad_request"
            ? t("library.metadata.badQuery")
            : t("common.loadError")}
        </p>
      )}
      {failed.length > 0 && (
        <p className="text-sm text-muted-foreground">
          {t("library.metadata.providerFailed", { names: failed.join(", ") })}
        </p>
      )}
      {found.isSuccess && !found.isFetching && found.data.items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("library.metadata.nothing")}</p>
      )}
      <ul className="flex max-h-[45dvh] flex-col gap-1 overflow-y-auto">
        {found.data?.items.map((c, i) => (
          <li key={`${c.provider}-${i}`}>
            <button
              type="button"
              className="flex w-full min-w-0 gap-3 rounded-lg px-2 py-2 text-left hover:bg-accent"
              onClick={() => onPick(c)}
            >
              <CandidateCover url={c.cover_url} />
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="text-sm font-medium [overflow-wrap:anywhere]">
                  {c.title ?? t("library.metadata.noTitle")}
                </span>
                {c.authors && <span className="truncate text-xs text-muted-foreground">{c.authors}</span>}
                <span className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                  {[providerName(t, c.provider), c.year].filter(Boolean).join(" · ")}
                  {c.match === "isbn" && <Badge variant="secondary">ISBN</Badge>}
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
      {footer}
    </>
  );
}

function Field({ id, label, children }: { id: string; label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
    </div>
  );
}

// Непустые поля кандидата заменяют текущие значения, остальные остаются как есть.
function initialForm(detail: LibraryBookDetail, c: LibraryMetadataCandidate | null) {
  const { book } = detail;
  const series = c?.series ? { name: c.series, index: c.series_index } : { name: book.series, index: book.series_index };
  return {
    title: c?.title ?? book.title ?? "",
    authors: c?.authors ?? book.authors ?? "",
    series: series.name ?? "",
    seriesIndex: series.index === null || series.index === undefined ? "" : String(series.index),
    language: c?.language ?? book.language ?? "",
    description: c?.description ?? detail.description ?? "",
  };
}

function MetadataForm({
  detail,
  candidate,
  onBack,
  onSaved,
}: {
  detail: LibraryBookDetail;
  candidate: LibraryMetadataCandidate | null;
  onBack: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState(() => initialForm(detail, candidate));
  const [useCover, setUseCover] = useState(!!candidate?.cover_url);
  const save = useSaveMetadata(detail.book.id);
  const set = (key: keyof typeof form) => (e: { target: { value: string } }) =>
    setForm((f) => ({ ...f, [key]: e.target.value }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const index = form.seriesIndex.trim() === "" ? null : Number(form.seriesIndex);
    const body: LibraryMetadataForm = {
      title: form.title,
      authors: form.authors,
      series: form.series,
      series_index: index,
      language: form.language,
      description: form.description,
      source: candidate?.provider ?? null,
      cover_url: useCover ? candidate?.cover_url : undefined,
    };
    save.mutate(body, { onSuccess: onSaved });
  };

  return (
    <form className="grid gap-3" onSubmit={submit}>
      {candidate?.cover_url && (
        <label className="flex items-center gap-3 text-sm">
          <CandidateCover url={candidate.cover_url} />
          <input type="checkbox" checked={useCover} onChange={(e) => setUseCover(e.target.checked)} />
          {t("library.metadata.useCover")}
        </label>
      )}
      <Field id="meta-title" label={t("library.metadata.titleField")}>
        <Input id="meta-title" maxLength={500} value={form.title} onChange={set("title")} />
      </Field>
      <Field id="meta-authors" label={t("library.metadata.authors")}>
        <Input id="meta-authors" maxLength={1000} value={form.authors} onChange={set("authors")} />
      </Field>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_6rem_6rem]">
        <Field id="meta-series" label={t("library.detail.series")}>
          <Input id="meta-series" maxLength={500} value={form.series} onChange={set("series")} />
        </Field>
        <Field id="meta-index" label={t("library.metadata.seriesIndex")}>
          <Input id="meta-index" type="number" min="0" step="any" value={form.seriesIndex} onChange={set("seriesIndex")} />
        </Field>
        <Field id="meta-language" label={t("library.detail.language")}>
          <Input id="meta-language" maxLength={16} value={form.language} onChange={set("language")} />
        </Field>
      </div>
      <Field id="meta-description" label={t("library.metadata.description")}>
        <Textarea
          id="meta-description"
          className="max-h-60"
          maxLength={20000}
          value={form.description}
          onChange={set("description")}
        />
      </Field>
      <FormError message={save.isError ? libraryErrorText(t, save.error) : undefined} />
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onBack}>
          {t("library.detail.mergeBack")}
        </Button>
        <Button type="submit" disabled={save.isPending}>
          {t("library.metadata.save")}
        </Button>
      </DialogFooter>
    </form>
  );
}

function ResetButton({ bookId, onDone }: { bookId: number; onDone: () => void }) {
  const { t } = useTranslation();
  const [confirming, setConfirming] = useState(false);
  const clear = useClearMetadata(bookId);
  if (!confirming) {
    return (
      <Button type="button" variant="ghost" onClick={() => setConfirming(true)}>
        {t("library.metadata.reset")}
      </Button>
    );
  }
  return (
    <>
      <FormError message={clear.isError ? libraryErrorText(t, clear.error) : undefined} />
      <Button
        type="button"
        variant="destructive"
        disabled={clear.isPending}
        onClick={() => clear.mutate(undefined, { onSuccess: onDone })}
      >
        {t("library.metadata.resetConfirm")}
      </Button>
    </>
  );
}

export function MetadataDialog({ detail, onClose }: { detail: LibraryBookDetail; onClose: () => void }) {
  const { t } = useTranslation();
  // undefined — список кандидатов, null — форма без кандидата.
  const [picked, setPicked] = useState<LibraryMetadataCandidate | null | undefined>(undefined);
  const { book } = detail;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] grid-cols-[minmax(0,1fr)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="[overflow-wrap:anywhere]">
            {t("library.metadata.title", { title: displayTitle(book) })}
          </DialogTitle>
          <DialogDescription>
            {picked === undefined ? t("library.metadata.pickHint") : t("library.metadata.formHint")}
          </DialogDescription>
        </DialogHeader>
        {picked === undefined ? (
          <CandidateList
            bookId={book.id}
            onPick={setPicked}
            footer={
              <DialogFooter>
                {detail.metadata && <ResetButton bookId={book.id} onDone={onClose} />}
                <Button type="button" variant="outline" onClick={() => setPicked(null)}>
                  {t("library.metadata.manual")}
                </Button>
              </DialogFooter>
            }
          />
        ) : (
          <MetadataForm detail={detail} candidate={picked} onBack={() => setPicked(undefined)} onSaved={onClose} />
        )}
      </DialogContent>
    </Dialog>
  );
}
