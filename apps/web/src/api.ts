import { type ApiError, createApiClient, type Middleware } from "@ksb/api-client";

// Один клиент на приложение: панель и API живут на одном origin.
export const api = createApiClient();

/** Ошибка API с машинным кодом; retryAfter — секунды из Retry-After. */
export class ApiFailure extends Error {
  readonly code: string;
  readonly retryAfter?: number;

  constructor(code: string, retryAfter?: number) {
    super(code);
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

export function failure(error: ApiError | undefined, response: Response): ApiFailure {
  const retry = Number(response.headers.get("Retry-After"));
  return new ApiFailure(error?.code ?? "unknown", Number.isFinite(retry) && retry > 0 ? retry : undefined);
}

// 401 на защищённом запросе значит, что сессии больше нет (выход в другой
// вкладке, смена пароля, отключение пользователя). Вход отвечает 401 на неверный
// пароль, а /me разбирает 401 сам в проверке маршрута — их не трогаем.
const ownUnauthorized = new Set(["/api/v1/auth/login", "/api/v1/me"]);

let onUnauthorized: () => void = () => {};

/** Задаёт реакцию на потерю сессии; вызывается один раз при старте. */
export function setUnauthorizedHandler(handler: () => void) {
  onUnauthorized = handler;
}

const sessionMiddleware: Middleware = {
  onResponse({ response, schemaPath }) {
    if (response.status === 401 && !ownUnauthorized.has(schemaPath)) onUnauthorized();
  },
};

api.use(sessionMiddleware);
