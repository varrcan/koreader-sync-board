import type {
  ReadingSessionPage,
  StatisticsCalendar,
  StatisticsPeriod,
  StatisticsState,
  StatisticsSummary,
} from "@ksb/api-client";
import { infiniteQueryOptions, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, failure } from "./api";

export const statisticsQuery = queryOptions({
  queryKey: ["statistics"],
  queryFn: async (): Promise<StatisticsState> => {
    const { data, error, response } = await api.GET("/api/v1/statistics");
    if (error) throw failure(error, response);
    return data;
  },
});

/** Пояс браузера: дни и часы статистики считаются по нему. */
export const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

export function summaryQuery(period: StatisticsPeriod) {
  return queryOptions({
    queryKey: ["statistics", "summary", period, timeZone],
    queryFn: async (): Promise<StatisticsSummary> => {
      const { data, error, response } = await api.GET("/api/v1/statistics/summary", {
        params: { query: { period, tz: timeZone } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

export const calendarQuery = queryOptions({
  queryKey: ["statistics", "calendar", timeZone],
  queryFn: async (): Promise<StatisticsCalendar> => {
    const { data, error, response } = await api.GET("/api/v1/statistics/calendar", {
      params: { query: { tz: timeZone } },
    });
    if (error) throw failure(error, response);
    return data;
  },
});

export function sessionsQuery(limit = 50) {
  return infiniteQueryOptions({
    queryKey: ["statistics", "sessions", limit],
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: ReadingSessionPage) => last.next_cursor ?? undefined,
    queryFn: async ({ pageParam }): Promise<ReadingSessionPage> => {
      const { data, error, response } = await api.GET("/api/v1/statistics/sessions", {
        params: { query: { limit, cursor: pageParam } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

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
      // По префиксу — итоги, календарь и сессии.
      void client.invalidateQueries({ queryKey: ["statistics"] });
      // Названия из статистики меняют книги и ленту.
      void client.invalidateQueries({ queryKey: ["books"] });
      void client.invalidateQueries({ queryKey: ["book"] });
      void client.invalidateQueries({ queryKey: ["activity"] });
    },
  });
}
