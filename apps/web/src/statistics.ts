import type { StatisticsState } from "@ksb/api-client";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, failure } from "./api";

export const statisticsQuery = queryOptions({
  queryKey: ["statistics"],
  queryFn: async (): Promise<StatisticsState> => {
    const { data, error, response } = await api.GET("/api/v1/statistics");
    if (error) throw failure(error, response);
    return data;
  },
});

/** Загрузка statistics.sqlite3: тело — сам файл, без JSON и multipart. */
export function useImportStatistics() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (file: File): Promise<StatisticsState> => {
      const { data, error, response } = await api.POST("/api/v1/statistics/import", {
        body: file,
        // openapi-fetch пропускает как есть только FormData, остальное сериализует в JSON.
        bodySerializer: (b) => b,
        headers: { "Content-Type": "application/octet-stream" },
      });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (state) => {
      client.setQueryData(statisticsQuery.queryKey, state);
      // Названия из статистики меняют книги и ленту.
      void client.invalidateQueries({ queryKey: ["books"] });
      void client.invalidateQueries({ queryKey: ["book"] });
      void client.invalidateQueries({ queryKey: ["activity"] });
    },
  });
}
