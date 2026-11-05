import type { ActivityPage, BookDetail, BookFilter, BookPage } from "@ksb/api-client";
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api, ApiFailure, failure } from "./api";

export function booksQuery(filter: BookFilter | undefined, limit = 50) {
  return infiniteQueryOptions({
    queryKey: ["books", filter ?? null, limit],
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: BookPage) => last.next_cursor ?? undefined,
    queryFn: async ({ pageParam }): Promise<BookPage> => {
      const { data, error, response } = await api.GET("/api/v1/books", {
        params: { query: { filter, limit, cursor: pageParam } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

export function activityQuery(hash: string | undefined, limit = 50) {
  return infiniteQueryOptions({
    queryKey: ["activity", hash ?? null, limit],
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: ActivityPage) => last.next_cursor ?? undefined,
    queryFn: async ({ pageParam }): Promise<ActivityPage> => {
      const { data, error, response } = await api.GET("/api/v1/activity", {
        params: { query: { document: hash, limit, cursor: pageParam } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}

export function bookQuery(hash: string) {
  return queryOptions({
    queryKey: ["book", hash],
    // Чужая или несуществующая книга — окончательный ответ, повторять незачем.
    retry: (count, err) => !(err instanceof ApiFailure && err.code === "not_found") && count < 3,
    queryFn: async (): Promise<BookDetail> => {
      const { data, error, response } = await api.GET("/api/v1/books/{hash}", {
        params: { path: { hash } },
      });
      if (error) throw failure(error, response);
      return data;
    },
  });
}
