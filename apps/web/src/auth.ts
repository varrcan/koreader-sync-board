import type { Me, SetupStatus } from "@ksb/api-client";
import { QueryClient, queryOptions } from "@tanstack/react-query";
import type { useTranslation } from "react-i18next";
import { api, ApiFailure, failure } from "./api";

export const queryClient = new QueryClient();

// Сессия меняется только входом, выходом и 401 — всё это обновляет кеш явно,
// поэтому перезапрашивать me и setup по таймеру не нужно.
export const meQuery = queryOptions({
  queryKey: ["me"],
  staleTime: Infinity,
  queryFn: async (): Promise<Me | null> => {
    const { data, error, response } = await api.GET("/api/v1/me");
    if (response.status === 401) return null;
    if (error) throw failure(error, response);
    return data;
  },
});

export const setupQuery = queryOptions({
  queryKey: ["setup"],
  staleTime: Infinity,
  queryFn: async (): Promise<SetupStatus> => {
    const { data, error, response } = await api.GET("/api/v1/setup");
    if (error) throw failure(error, response);
    return data;
  },
});

/** Сессия открыта: кладёт пользователя в кеш, мастер больше не нужен. */
export function signedIn(me: Me) {
  queryClient.setQueryData(meQuery.queryKey, me);
  queryClient.setQueryData(setupQuery.queryKey, { required: false });
}

export function signedOut() {
  queryClient.setQueryData(meQuery.queryKey, null);
}

/**
 * Куда вернуться после входа: только путь внутри панели. «//host» и «/\host»
 * браузер понимает как адрес другого сайта.
 */
export function safeRedirect(target: unknown): string {
  if (typeof target !== "string" || !target.startsWith("/") || target.startsWith("//") || target.startsWith("/\\")) {
    return "/";
  }
  return target;
}

type TFunction = ReturnType<typeof useTranslation>["t"];

/** Текст ошибки API для пользователя. */
export function errorText(t: TFunction, err: unknown): string {
  const code = err instanceof ApiFailure ? err.code : "unknown";
  switch (code) {
    case "invalid_credentials":
    case "invalid_password":
    case "invalid_username":
    case "invalid_setup_code":
    case "setup_completed":
    case "cross_origin":
    case "not_found":
    case "user_not_found":
    case "forbidden":
    case "device_exists":
    case "invalid_device_name":
      return t(`errors.${code}`);
    case "too_many_attempts": {
      const seconds = err instanceof ApiFailure ? (err.retryAfter ?? 900) : 900;
      return t("errors.too_many_attempts", { minutes: Math.ceil(seconds / 60) });
    }
    default:
      return t("errors.unknown");
  }
}
