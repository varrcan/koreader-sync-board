import { Link } from "@tanstack/react-router";
import { CircleAlert, CircleCheck, LoaderCircle, Upload, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Value, webdavAddress } from "@/components/KoreaderSetup";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  UploadFailure,
  uploadAccept,
  uploadBook,
  uploadErrorText,
  uploadProblem,
  useRefreshAfterScan,
} from "@/library";

type Item = {
  id: number;
  file: File;
  status: "waiting" | "uploading" | "done" | "error";
  progress: number;
  name?: string;
  error?: unknown;
};

/**
 * Очередь загрузки: файлы уходят по одному в порядке добавления. Файлы, которые
 * сервер всё равно отклонит, отмечаются сразу и не отправляются.
 */
export function useUploadQueue() {
  const [items, setItems] = useState<Item[]>([]);
  const queue = useRef<Item[]>([]);
  const running = useRef(false);
  const nextId = useRef(0);
  const refresh = useRefreshAfterScan();

  const update = useCallback((id: number, patch: Partial<Item>) => {
    setItems((prev) => prev.map((it) => (it.id === id ? { ...it, ...patch } : it)));
  }, []);

  const pump = useCallback(async () => {
    if (running.current) return;
    running.current = true;
    let uploaded = false;
    for (let it = queue.current.shift(); it; it = queue.current.shift()) {
      const id = it.id;
      update(id, { status: "uploading", progress: 0 });
      try {
        const res = await uploadBook(it.file, (progress) => update(id, { progress }));
        update(id, { status: "done", progress: 1, name: res.name });
        uploaded = true;
      } catch (error) {
        update(id, { status: "error", error });
      }
    }
    running.current = false;
    if (uploaded) refresh();
  }, [refresh, update]);

  const add = useCallback(
    (files: Iterable<File>) => {
      const added: Item[] = [];
      for (const file of files) {
        const problem = uploadProblem(file);
        added.push({
          id: nextId.current++,
          file,
          progress: 0,
          ...(problem ? { status: "error", error: new UploadFailure(problem) } : { status: "waiting" }),
        });
      }
      setItems((prev) => [...prev, ...added]);
      queue.current.push(...added.filter((it) => it.status === "waiting"));
      void pump();
    },
    [pump],
  );

  const busy = items.some((it) => it.status === "waiting" || it.status === "uploading");
  const clear = useCallback(
    () => setItems((prev) => prev.filter((it) => it.status === "waiting" || it.status === "uploading")),
    [],
  );

  // Уход со страницы прервал бы передачу.
  useEffect(() => {
    if (!busy) return;
    const onBeforeUnload = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [busy]);

  return { items, add, clear, busy };
}

export function UploadButton({ onFiles }: { onFiles: (files: File[]) => void }) {
  const { t } = useTranslation();
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <input
        ref={input}
        type="file"
        multiple
        accept={uploadAccept}
        className="hidden"
        onChange={(e) => {
          onFiles(Array.from(e.target.files ?? []));
          e.target.value = "";
        }}
      />
      <Button onClick={() => input.current?.click()}>
        <Upload />
        {t("library.upload.button")}
      </Button>
    </>
  );
}

const hasFiles = (e: DragEvent) => e.dataTransfer?.types.includes("Files") ?? false;

/** Принимает файлы, перетащенные в любое место окна, и подсвечивает экран. */
export function DropTarget({ onFiles }: { onFiles: (files: File[]) => void }) {
  const { t } = useTranslation();
  const [over, setOver] = useState(false);
  useEffect(() => {
    // dragenter и dragleave приходят от каждого вложенного элемента: считаем глубину.
    let depth = 0;
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth++;
      setOver(true);
    };
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth = Math.max(0, depth - 1);
      if (depth === 0) setOver(false);
    };
    const overHandler = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
    };
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      setOver(false);
      onFiles(Array.from(e.dataTransfer?.files ?? []));
    };
    window.addEventListener("dragenter", enter);
    window.addEventListener("dragleave", leave);
    window.addEventListener("dragover", overHandler);
    window.addEventListener("drop", drop);
    return () => {
      window.removeEventListener("dragenter", enter);
      window.removeEventListener("dragleave", leave);
      window.removeEventListener("dragover", overHandler);
      window.removeEventListener("drop", drop);
    };
  }, [onFiles]);
  if (!over) return null;
  return (
    <div className="pointer-events-none fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4">
      <div className="flex max-w-md flex-col items-center gap-3 rounded-2xl border-2 border-dashed border-primary bg-card px-8 py-10 text-center">
        <Upload className="size-8 text-primary" />
        <p className="text-base font-medium text-pretty">{t("library.upload.drop")}</p>
      </div>
    </div>
  );
}

function ItemRow({ item }: { item: Item }) {
  const { t } = useTranslation();
  const bookId = item.error instanceof UploadFailure && item.error.code === "file_exists" ? item.error.bookId : undefined;
  return (
    <li className="flex flex-col gap-1.5 border-t py-2.5 first:border-t-0">
      <div className="flex min-w-0 items-center gap-2 text-sm [&_svg]:size-4 [&_svg]:shrink-0">
        {item.status === "done" && <CircleCheck className="text-primary" />}
        {item.status === "error" && <CircleAlert className="text-destructive" />}
        {item.status === "uploading" && <LoaderCircle className="animate-spin text-muted-foreground" />}
        {item.status === "waiting" && <span className="size-4 shrink-0" />}
        <span className="min-w-0 flex-1 truncate">{item.file.name}</span>
        {item.status === "uploading" && (
          <span className="text-xs text-muted-foreground tabular-nums">{Math.round(item.progress * 100)} %</span>
        )}
        {item.status === "waiting" && <span className="text-xs text-muted-foreground">{t("library.upload.waiting")}</span>}
      </div>
      {item.status === "uploading" && (
        <div className="ml-6 h-1 overflow-hidden rounded-full bg-muted">
          <div className="h-full bg-primary transition-[width]" style={{ width: `${item.progress * 100}%` }} />
        </div>
      )}
      {item.status === "done" && (
        <p className="ml-6 text-xs text-muted-foreground">
          {item.name && item.name !== item.file.name.normalize("NFC")
            ? t("library.upload.doneAs", { name: item.name })
            : t("library.upload.done")}
        </p>
      )}
      {item.status === "error" && (
        <p className="ml-6 text-xs text-destructive">
          {uploadErrorText(t, item.error)}
          {bookId !== undefined && (
            <>
              {" "}
              <Link
                to="/library/books/$id"
                params={{ id: String(bookId) }}
                className="font-medium text-foreground underline underline-offset-2"
              >
                {t("library.upload.openBook")}
              </Link>
            </>
          )}
        </p>
      )}
    </li>
  );
}

export function UploadQueue({ items, busy, onClear }: { items: Item[]; busy: boolean; onClear: () => void }) {
  const { t } = useTranslation();
  if (items.length === 0) return null;
  const finished = items.some((it) => it.status === "done" || it.status === "error");
  return (
    <section className="flex max-w-5xl flex-col gap-1 rounded-xl border bg-card p-4">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-[15px] leading-5 font-semibold">{t("library.upload.title")}</h2>
        {finished && (
          <Button variant="ghost" size="sm" onClick={onClear}>
            <X />
            {t(busy ? "library.upload.clearFinished" : "library.upload.clear")}
          </Button>
        )}
      </div>
      <ul className="flex flex-col">
        {items.map((it) => (
          <ItemRow key={it.id} item={it} />
        ))}
      </ul>
    </section>
  );
}

/** Адрес WebDAV-диска книг и как к нему подключиться. */
export function WebdavBooks({ className }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <Value label={t("library.upload.webdavAddress")} value={`${webdavAddress()}/books/`} wrap />
      <p className="text-xs text-pretty text-muted-foreground">{t("library.upload.webdavHint")}</p>
    </div>
  );
}
