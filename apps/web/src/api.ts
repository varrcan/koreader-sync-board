import { createApiClient } from "@ksb/api-client";

// Один клиент на приложение: панель и API живут на одном origin.
export const api = createApiClient();
