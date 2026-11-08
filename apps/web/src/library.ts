import type { LibraryBookDetail, LibraryBookPage, LibraryRoot, LibrarySummary } from "@ksb/api-client";
import {
  infiniteQueryOptions,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { api, ApiFailure, failure } from "./api";
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

export function libraryBooksQuery(q: string, limit = 50) {
  return infiniteQueryOptions({
    queryKey: ["library", "books", "list", q, limit],
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: LibraryBookPage) => last.next_cursor ?? undefined,
    queryFn: async ({ pageParam }): Promise<LibraryBookPage> => {
      const { data, error, response } = await api.GET("/api/v1/library/books", {
        params: { query: { q: q || undefined, limit, cursor: pageParam } },
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

type TFunction = Parameters<typeof errorText>[0];

/** errorText, где not_found — про библиотеку: общий текст говорит об устройстве. */
export function libraryErrorText(t: TFunction, err: unknown): string {
  return err instanceof ApiFailure && err.code === "not_found" ? t("errors.library_not_found") : errorText(t, err);
}
