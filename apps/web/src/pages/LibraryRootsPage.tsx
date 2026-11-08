import type { LibraryRoot, LibrarySummary } from "@ksb/api-client";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, FolderOpen, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { errorText } from "@/auth";
import { FormError } from "@/components/FormError";
import { ScanStatus } from "@/components/ScanStatus";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { formatRelative } from "@/lib/format";
import {
  libraryErrorText,
  useAddRoot,
  useDeleteRoot,
  useLibraryRoots,
  useLibrarySummary,
  useScanLibrary,
} from "@/library";

function SummaryCard({ summary, now }: { summary: LibrarySummary; now: number }) {
  const { t } = useTranslation();
  const stats = [
    { label: t("library.books"), value: summary.books },
    { label: t("library.files"), value: summary.files },
    { label: t("library.roots"), value: summary.roots },
  ];
  return (
    <Card className="max-w-3xl gap-4 px-6">
      <dl className="grid grid-cols-3 gap-4">
        {stats.map((s) => (
          <div key={s.label} className="flex flex-col gap-0.5">
            <dt className="text-xs text-muted-foreground">{s.label}</dt>
            <dd className="font-serif text-2xl tabular-nums">{s.value}</dd>
          </div>
        ))}
      </dl>
      <ScanStatus scan={summary.scan} now={now} />
    </Card>
  );
}

function RootRow({ root, now, onDelete }: { root: LibraryRoot; now: number; onDelete: () => void }) {
  const { t, i18n } = useTranslation();
  const meta = [
    t("library.rootFiles", { files: root.files }),
    root.scanned_at
      ? t("library.rootScanned", { when: formatRelative(root.scanned_at, i18n.language, now) })
      : t("library.rootNotScanned"),
  ];
  return (
    <li className="flex items-start gap-3 px-4 py-3.5">
      <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-secondary text-muted-foreground [&_svg]:size-4.5">
        <FolderOpen />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate font-medium">{root.name}</span>
        <p className="truncate font-mono text-xs text-muted-foreground" title={root.path}>
          {root.path}
        </p>
        <p className="text-xs text-muted-foreground">{meta.join(" · ")}</p>
        {root.error && <p className="text-xs break-words text-destructive">{root.error}</p>}
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        className="shrink-0 hover:text-destructive"
        title={t("library.delete")}
        aria-label={`${t("library.delete")}: ${root.name}`}
        onClick={onDelete}
      >
        <Trash2 />
      </Button>
    </li>
  );
}

function AddRootForm() {
  const { t } = useTranslation();
  const [path, setPath] = useState("");
  const [name, setName] = useState("");
  const add = useAddRoot();
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { path: path.trim(), name: name.trim() || undefined },
          {
            onSuccess: () => {
              setPath("");
              setName("");
            },
          },
        );
      }}
    >
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
        <div className="flex flex-col gap-2">
          <Label htmlFor="root-path">{t("library.path")}</Label>
          <Input
            id="root-path"
            required
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            placeholder="/books"
            aria-describedby="root-path-hint"
            value={path}
            onChange={(e) => setPath(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="root-name">{t("library.name")}</Label>
          <Input
            id="root-name"
            autoComplete="off"
            maxLength={100}
            placeholder={t("library.namePlaceholder")}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
      </div>
      <p id="root-path-hint" className="text-sm text-pretty text-muted-foreground">
        {t("library.pathHint")}
      </p>
      <FormError message={add.isError ? errorText(t, add.error) : undefined} />
      <Button type="submit" className="self-start" disabled={add.isPending}>
        <Plus />
        {t("library.add")}
      </Button>
    </form>
  );
}

function DeleteRootDialog({ root, onClose }: { root: LibraryRoot; onClose: () => void }) {
  const { t } = useTranslation();
  const remove = useDeleteRoot();
  return (
    <AlertDialog open onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("library.deleteDialog.title", { name: root.name })}</AlertDialogTitle>
          <AlertDialogDescription>{t("library.deleteDialog.description")}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormError message={remove.isError ? libraryErrorText(t, remove.error) : undefined} />
        <AlertDialogFooter>
          <AlertDialogCancel>{t("library.cancel")}</AlertDialogCancel>
          <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate(root.id, { onSuccess: onClose })}>
            {t("library.deleteDialog.confirm")}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function RootsCard() {
  const { t } = useTranslation();
  const roots = useLibraryRoots();
  const [deleting, setDeleting] = useState<LibraryRoot | null>(null);
  return (
    <Card className="max-w-3xl gap-5 px-6">
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-medium">{t("library.rootsTitle")}</h2>
        <p className="text-sm text-pretty text-muted-foreground">{t("library.rootsDescription")}</p>
      </div>
      {roots.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {roots.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
      {roots.isSuccess && roots.data.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("library.noRoots")}</p>
      )}
      {roots.isSuccess && roots.data.length > 0 && (
        <ul className="-mx-4 divide-y border-y">
          {roots.data.map((r) => (
            <RootRow key={r.id} root={r} now={roots.dataUpdatedAt} onDelete={() => setDeleting(r)} />
          ))}
        </ul>
      )}
      <AddRootForm />
      {deleting && <DeleteRootDialog root={deleting} onClose={() => setDeleting(null)} />}
    </Card>
  );
}

export function LibraryRootsPage() {
  const { t } = useTranslation();
  const summary = useLibrarySummary();
  const scan = useScanLibrary();
  const running = summary.data?.scan.running ?? false;

  return (
    <>
      <Link
        to="/library"
        className="flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground [&_svg]:size-4"
      >
        <ArrowLeft />
        {t("library.title")}
      </Link>
      <div className="flex max-w-3xl flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("library.rootsTitle")}</h1>
        {(summary.data?.roots ?? 0) > 0 && (
          <Button variant="outline" className="self-start" disabled={scan.isPending} onClick={() => scan.mutate()}>
            <RefreshCw className={running ? "animate-spin" : undefined} />
            {t("library.scan")}
          </Button>
        )}
      </div>
      <FormError message={scan.isError ? errorText(t, scan.error) : undefined} />

      {summary.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {summary.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}
      {summary.data && <SummaryCard summary={summary.data} now={summary.dataUpdatedAt} />}
      <RootsCard />
    </>
  );
}
