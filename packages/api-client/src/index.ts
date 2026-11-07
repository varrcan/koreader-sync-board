// Типизированный клиент API панели поверх openapi-fetch.
// Типы берутся из src/schema.d.ts, сгенерированного по api/openapi.yaml.
import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export type { components, paths };
export type { Middleware } from "openapi-fetch";

type Schemas = components["schemas"];

export type Health = Schemas["Health"];
export type Version = Schemas["Version"];
export type ApiError = Schemas["Error"];
export type Me = Schemas["Me"];
export type SetupStatus = Schemas["SetupStatus"];
export type Device = Schemas["Device"];
export type DeviceWithPassword = Schemas["DeviceWithPassword"];
export type UserSummary = Schemas["UserSummary"];
export type BookFilter = Schemas["BookFilter"];
export type Book = Schemas["Book"];
export type BookCounts = Schemas["BookCounts"];
export type BookPage = Schemas["BookPage"];
export type BookDevice = Schemas["BookDevice"];
export type BookDetail = Schemas["BookDetail"];
export type ActivityItem = Schemas["ActivityItem"];
export type ActivityPage = Schemas["ActivityPage"];
export type StatisticsSnapshot = Schemas["StatisticsSnapshot"];
export type StatisticsState = Schemas["StatisticsState"];
export type StatisticsPeriod = Schemas["StatisticsPeriod"];
export type StatisticsTotals = Schemas["StatisticsTotals"];
export type StatisticsBucket = Schemas["StatisticsBucket"];
export type StatisticsBookRef = Schemas["StatisticsBookRef"];
export type StatisticsBook = Schemas["StatisticsBook"];
export type StatisticsSummary = Schemas["StatisticsSummary"];
export type StatisticsDay = Schemas["StatisticsDay"];
export type StatisticsCalendar = Schemas["StatisticsCalendar"];
export type ReadingSession = Schemas["ReadingSession"];
export type ReadingSessionPage = Schemas["ReadingSessionPage"];

export type ApiClient = ReturnType<typeof createApiClient>;

/**
 * Создаёт клиент API. По умолчанию запросы идут на тот же origin,
 * откуда отдана панель (SPA вшита в Go-бинарник).
 */
export function createApiClient(baseUrl = "") {
  return createClient<paths>({ baseUrl });
}
