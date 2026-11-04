import { initI18n } from "@ksb/i18n";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { setUnauthorizedHandler } from "./api";
import { queryClient, signedOut } from "./auth";
import { router } from "./router";
import "./styles.css";

initI18n();

// Сессия пропала посреди работы: забываем пользователя и уходим на вход,
// чтобы после него вернуться туда же.
setUnauthorizedHandler(() => {
  signedOut();
  const { pathname, searchStr } = router.state.location;
  if (pathname !== "/login") void router.navigate({ to: "/login", search: { redirect: pathname + searchStr } });
});

const root = document.getElementById("root");
if (!root) throw new Error("#root не найден");

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
