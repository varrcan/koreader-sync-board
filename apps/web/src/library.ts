import type {
  ApiError,
  LibraryBookDetail,
  LibraryBookPage,
  LibraryMetadataCandidates,
  LibraryMetadataForm,
  LibraryRoot,
  LibrarySummary,
  LibraryUploadConflict,
  LibraryUploadResult,
} from "@ksb/api-client";
import {
  infiniteQueryOptions,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useRef } from "react";
import { api, ApiFailure, failure, sessionLost } from "./api";
import { errorText } from "./auth";

// Пока идёт скан, сводка и корни опрашиваются: число файлов и ошибки меняются.
const scanPoll = 2000;

export const librarySummaryQuery = queryOptions({
  queryKey: ["library"],
  refetchInterval: (q) => (q.state.data?.scan.running ? scanPoll : false),
  queryFn: async (): Promise<LibrarySummary> => {
    const { data, error, response } = await api.GET("/api/v1/library");
    if (error) throw failure(error, response);
    return data;
  },
});

const libraryRootsQuery = queryOptions({
  queryKey: ["library", "roots"],
  queryFn: async (): Promise<LibraryRoot[]> => {
    const { data, error, response } = await api.GET("/api/v1/library/roots");
    if (error) throw failure(error, response);
    return data.items;
  },
});

export function libraryBooksQuery(q: string, limit = 50, suggested = false) {
  return infiniteQueryOptions({
    queryKey: ["library", "books", "list", q, limit, suggested],
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: LibraryBookPage) => last.next_cursor ?? undefined,
    queryFn: async ({ pageParam }): Promise<LibraryBookPage> => {
      const { data, error, response } = await api.GET("/api/v1/library/books", {
        params: { query: { q: q || undefined, suggested: suggested || undefined, limit, cursor: pageParam } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

export function libraryBookQuery(id: number) {
  return queryOptions({
    queryKey: ["library", "books", "detail", id],
    // Несуществующая книга — окончательный ответ, повторять незачем.
    retry: (count, err) => !(err instanceof ApiFailure && err.code === "not_found") && count < 3,
    queryFn: async (): Promise<LibraryBookDetail> => {
      const { data, error, response } = await api.GET("/api/v1/library/books/{id}", {
        params: { path: { id } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

/** Корни; пока идёт скан (по сводке), опрашиваются вместе с ней. */
export function useLibraryRoots() {
  const { data: summary } = useQuery(librarySummaryQuery);
  return useQuery({ ...libraryRootsQuery, refetchInterval: summary?.scan.running ? scanPoll : false });
}

/** Названия книг прогресса могут прийти из библиотеки: книги, карточки и лента устаревают. */
function invalidateProgress(client: QueryClient) {
  void client.invalidateQueries({ queryKey: ["books"] });
  void client.invalidateQueries({ queryKey: ["book"] });
  void client.invalidateQueries({ queryKey: ["activity"] });
}

/**
 * После загрузок ждёт конца скана загрузок и обновляет каталог. Скан маленькой папки
 * успевает закончиться до запроса сводки, и useLibrarySummary его конец не увидит,
 * поэтому ждётся сводка, полученная позже вызова, без идущего скана.
 */
export function useRefreshAfterScan() {
  const client = useQueryClient();
  const summary = useQuery(librarySummaryQuery);
  const since = useRef<number | null>(null);
  const { dataUpdatedAt } = summary;
  const running = summary.data?.scan.running;
  useEffect(() => {
    if (since.current === null || dataUpdatedAt < since.current || running !== false) return;
    since.current = null;
    void client.invalidateQueries({ queryKey: ["library", "books"] });
    invalidateProgress(client);
  }, [client, dataUpdatedAt, running]);
  return useCallback(() => {
    since.current = Date.now();
    void client.invalidateQueries({ queryKey: librarySummaryQuery.queryKey, exact: true });
  }, [client]);
}

/** Сводка библиотеки; по окончании скана обновляет корни, каталог и названия книг прогресса. */
export function useLibrarySummary() {
  const client = useQueryClient();
  const summary = useQuery(librarySummaryQuery);
  const running = summary.data?.scan.running;
  const wasRunning = useRef(running);
  useEffect(() => {
    if (wasRunning.current && running === false) {
      void client.invalidateQueries({ queryKey: ["library", "roots"] });
      void client.invalidateQueries({ queryKey: ["library", "books"] });
      invalidateProgress(client);
    }
    wasRunning.current = running;
  }, [client, running]);
  return summary;
}

export function useAddRoot() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: { path: string; name?: string }): Promise<LibraryRoot> => {
      const { data, error, response } = await api.POST("/api/v1/library/roots", { body });
      if (error) throw failure(error, response);
      return data;
    },
    // По префиксу — сводка (скан запущен) и корни. Скан маленького корня успевает
    // закончиться до запроса сводки, и опрос его конец не увидит.
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["library"] });
      invalidateProgress(client);
    },
  });
}

export function useDeleteRoot() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: number) => {
      const { error, response } = await api.DELETE("/api/v1/library/roots/{id}", { params: { path: { id } } });
      if (error) throw failure(error, response);
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["library"] });
      invalidateProgress(client);
    },
  });
}

export function useScanLibrary() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<LibrarySummary> => {
      const { data, error, response } = await api.POST("/api/v1/library/scan");
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (summary) => {
      client.setQueryData(librarySummaryQuery.queryKey, summary);
      void client.invalidateQueries({ queryKey: ["library", "roots"] });
    },
  });
}

/** Слияние и разделение меняют каталог, сводку и library_book_id у книг прогресса. */
function invalidateCatalog(client: QueryClient) {
  void client.invalidateQueries({ queryKey: ["library"] });
  invalidateProgress(client);
}

export function useMergeBooks() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async ({ into, from }: { into: number; from: number }): Promise<LibraryBookDetail> => {
      const { data, error, response } = await api.POST("/api/v1/library/books/{id}/merge", {
        params: { path: { id: into } },
        body: { book_id: from },
      });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: () => invalidateCatalog(client),
  });
}

export function useSplitFile() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: number): Promise<LibraryBookDetail> => {
      const { data, error, response } = await api.POST("/api/v1/library/files/{id}/split", {
        params: { path: { id } },
      });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: () => invalidateCatalog(client),
  });
}

export function useConvertFile() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: number): Promise<number> => {
      const { data, error, response } = await api.POST("/api/v1/library/files/{id}/convert", {
        params: { path: { id } },
      });
      if (error) throw failure(error, response);
      return data.file_id;
    },
    onSuccess: () => invalidateCatalog(client),
  });
}

/** Удаляет загруженный или сконвертированный файл. */
export function useDeleteFile() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: number): Promise<void> => {
      const { error, response } = await api.DELETE("/api/v1/library/files/{id}", {
        params: { path: { id } },
      });
      if (error) throw failure(error, response);
    },
    onSuccess: () => invalidateCatalog(client),
  });
}

export type CandidateSearch = { q?: string; isbn?: string };

/**
 * Каждый запрос — обращение к внешним каталогам с их лимитами, поэтому без
 * автоповтора и обновлений; префикс свой, чтобы инвалидация библиотеки его не задевала.
 */
export function libraryCandidatesQuery(id: number, search: CandidateSearch) {
  return queryOptions({
    queryKey: ["library-candidates", id, search],
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    queryFn: async (): Promise<LibraryMetadataCandidates> => {
      const { data, error, response } = await api.GET("/api/v1/library/books/{id}/metadata/candidates", {
        params: { path: { id }, query: search },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

/** Правка меняет название, обложку и поиск каталога и названия книг прогресса. */
function invalidateBook(client: QueryClient) {
  void client.invalidateQueries({ queryKey: ["library", "books"] });
  // Сохранение снимает отметку предложения.
  void client.invalidateQueries({ queryKey: ["settings"] });
  invalidateProgress(client);
}

export function useSaveMetadata(id: number) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: LibraryMetadataForm): Promise<LibraryBookDetail> => {
      const { data, error, response } = await api.PUT("/api/v1/library/books/{id}/metadata", {
        params: { path: { id } },
        body,
      });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: () => invalidateBook(client),
  });
}

/** Отклонение снимает отметку в книге и каталоге и меняет ход проверки. */
export function useDismissSuggestion(id: number) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<void> => {
      const { error, response } = await api.DELETE("/api/v1/library/books/{id}/metadata/suggestion", {
        params: { path: { id } },
      });
      if (error) throw failure(error, response);
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["library", "books"] });
      void client.invalidateQueries({ queryKey: ["settings"] });
    },
  });
}

export function useClearMetadata(id: number) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<LibraryBookDetail> => {
      const { data, error, response } = await api.DELETE("/api/v1/library/books/{id}/metadata", {
        params: { path: { id } },
      });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: () => invalidateBook(client),
  });
}

const fileContentUrl = (id: number) => `/api/v1/library/files/${id}/content`;

/**
 * Проверяет файл запросом HEAD и запускает загрузку обычной ссылкой: поток идёт в
 * менеджер загрузок браузера, а не через память вкладки. false — скачать нельзя.
 */
export async function downloadFile(id: number): Promise<boolean> {
  const head = await fetch(fileContentUrl(id), { method: "HEAD" }).catch(() => null);
  if (!head?.ok) return false;
  const link = document.createElement("a");
  link.href = fileContentUrl(id);
  link.download = "";
  link.click();
  return true;
}

/** Предел размера загружаемого файла — 1 ГиБ, как на сервере. */
export const maxUploadSize = 1 << 30;

// Тот же список, что у сканера; fb2.zip проверяется раньше zip-подобных.
const bookFormats = ["fb2.zip", "epub", "fb2", "pdf", "djvu", "mobi", "azw", "cbz"];

/** Значение accept для выбора файлов. */
export const uploadAccept = bookFormats.map((f) => `.${f.split(".").pop()}`).join(",");

/**
 * Код ошибки, которую сервер вернул бы файлу, или undefined. Проверяется до отправки:
 * сервер отвечает на большое тело, не дочитав его, и XHR видит обрыв, а не код.
 */
export function uploadProblem(file: File): string | undefined {
  const name = file.name.toLowerCase();
  if (file.name.startsWith(".")) return "invalid_name";
  if (!bookFormats.some((f) => name.endsWith(`.${f}`))) return "unsupported_format";
  if (file.size === 0) return "empty_file";
  if (file.size > maxUploadSize) return "file_too_large";
  return undefined;
}

/** Ошибка загрузки; bookId — книга с тем же содержимым при file_exists. */
export class UploadFailure extends ApiFailure {
  readonly bookId?: number;

  constructor(code: string, bookId?: number) {
    super(code);
    this.bookId = bookId;
  }
}

/**
 * Загружает файл книги. XMLHttpRequest, а не клиент API: у fetch нет прогресса выгрузки.
 * onProgress получает долю от 0 до 1.
 */
export function uploadBook(file: File, onProgress: (fraction: number) => void): Promise<LibraryUploadResult> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/v1/library/uploads?name=${encodeURIComponent(file.name)}`);
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.responseType = "json";
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      if (xhr.status === 201) {
        resolve(xhr.response as LibraryUploadResult);
        return;
      }
      if (xhr.status === 401) sessionLost();
      const body = xhr.response as (ApiError & Partial<LibraryUploadConflict>) | null;
      reject(new UploadFailure(body?.code ?? "unknown", body?.book_id));
    };
    xhr.onerror = () => reject(new UploadFailure("unknown"));
    xhr.send(file);
  });
}

type TFunction = Parameters<typeof errorText>[0];

const providerNames: Record<string, string> = {
  fantlab: "FantLab",
  openlibrary: "Open Library",
  google: "Google Books",
};

/** Подпись источника кандидата. */
export function providerName(t: TFunction, id: string): string {
  return providerNames[id] ?? t("library.metadata.fromFile");
}

/** Внешний источник правки; файл и ручная правка подписи не имеют. */
export function externalSource(source: string | null | undefined): string | undefined {
  return source ? providerNames[source] : undefined;
}

/** Текст ошибки загрузки: предел размера у загрузки свой. */
export function uploadErrorText(t: TFunction, err: unknown): string {
  if (err instanceof ApiFailure && err.code === "file_too_large") return t("library.upload.tooLarge");
  return libraryErrorText(t, err);
}

/** errorText, где not_found — про библиотеку: общий текст говорит об устройстве. */
export function libraryErrorText(t: TFunction, err: unknown): string {
  if (err instanceof ApiFailure && err.code === "not_found") return t("errors.library_not_found");
  // Тот же код у импорта статистики, но предел другой.
  if (err instanceof ApiFailure && err.code === "file_too_large") return t("errors.conversion_too_large");
  return errorText(t, err);
}
