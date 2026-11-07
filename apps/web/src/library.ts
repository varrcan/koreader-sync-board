import type { LibraryRoot, LibrarySummary } from "@ksb/api-client";
import { queryOptions, useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { api, failure } from "./api";

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

/** Сводка библиотеки; по окончании скана обновляет корни и названия книг прогресса. */
export function useLibrarySummary() {
  const client = useQueryClient();
  const summary = useQuery(librarySummaryQuery);
  const running = summary.data?.scan.running;
  const wasRunning = useRef(running);
  useEffect(() => {
    if (wasRunning.current && running === false) {
      void client.invalidateQueries({ queryKey: ["library", "roots"] });
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
