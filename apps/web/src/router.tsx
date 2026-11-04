// Code-based routing TanStack Router, без генератора маршрутов.
import { createRootRoute, createRoute, createRouter, Outlet, redirect } from "@tanstack/react-router";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, queryClient, safeRedirect, setupQuery } from "./auth";
import { AppLayout } from "./components/AppLayout";
import { ChangePasswordPage } from "./pages/ChangePasswordPage";
import { HomePage } from "./pages/HomePage";
import { LoginPage } from "./pages/LoginPage";
import { SetupPage } from "./pages/SetupPage";

function Root() {
  const { t, i18n } = useTranslation();
  useEffect(() => {
    document.documentElement.lang = i18n.language;
    document.title = t("app.title");
  }, [i18n.language, t]);
  return <Outlet />;
}

const rootRoute = createRootRoute({ component: Root });

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  beforeLoad: async () => {
    const setup = await queryClient.ensureQueryData(setupQuery);
    if (!setup.required) throw redirect({ to: "/" });
  },
  component: SetupPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (search: Record<string, unknown>): { redirect?: string } =>
    typeof search.redirect === "string" ? { redirect: search.redirect } : {},
  beforeLoad: async ({ search }) => {
    const setup = await queryClient.ensureQueryData(setupQuery);
    if (setup.required) throw redirect({ to: "/setup" });
    if (await queryClient.ensureQueryData(meQuery)) throw redirect({ href: safeRedirect(search.redirect) });
  },
  component: LoginPage,
});

// Защищённый layout: без пользователей — мастер, без сессии — вход с возвратом.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  beforeLoad: async ({ location }) => {
    const setup = await queryClient.ensureQueryData(setupQuery);
    if (setup.required) throw redirect({ to: "/setup" });
    const me = await queryClient.ensureQueryData(meQuery);
    if (!me) {
      const back = location.pathname === "/" && !location.searchStr ? undefined : location.href;
      throw redirect({ to: "/login", search: back ? { redirect: back } : {} });
    }
  },
  component: AppLayout,
});

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  component: HomePage,
});

const passwordRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/password",
  component: ChangePasswordPage,
});

const routeTree = rootRoute.addChildren([setupRoute, loginRoute, appRoute.addChildren([indexRoute, passwordRoute])]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
