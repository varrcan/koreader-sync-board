// Code-based routing TanStack Router, без генератора маршрутов.
import { createRootRoute, createRoute, createRouter, Outlet, redirect } from "@tanstack/react-router";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, queryClient, safeRedirect, setupQuery } from "./auth";
import type { BookFilter, StatisticsPeriod } from "@ksb/api-client";
import { AppLayout } from "./components/AppLayout";
import { BookPage } from "./pages/BookPage";
import { BooksPage } from "./pages/BooksPage";
import { ChangePasswordPage } from "./pages/ChangePasswordPage";
import { DevicesPage } from "./pages/DevicesPage";
import { HelpIndexPage } from "./pages/help/HelpIndexPage";
import { HelpProgressPage } from "./pages/help/HelpProgressPage";
import { HelpStatisticsPage } from "./pages/help/HelpStatisticsPage";
import { HelpTroubleshootingPage } from "./pages/help/HelpTroubleshootingPage";
import { HomePage } from "./pages/HomePage";
import { LoginPage } from "./pages/LoginPage";
import { SetupPage } from "./pages/SetupPage";
import { StatisticsPage } from "./pages/StatisticsPage";

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

const bookFilters: readonly string[] = ["reading", "finished", "unknown"] satisfies BookFilter[];

const booksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/books",
  // Неизвестный фильтр в адресе — просто все книги, а не 400 от сервера.
  validateSearch: (search: Record<string, unknown>): { filter?: BookFilter } =>
    typeof search.filter === "string" && bookFilters.includes(search.filter)
      ? { filter: search.filter as BookFilter }
      : {},
  component: BooksPage,
});

const bookRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/books/$hash",
  component: BookPage,
});

const statisticsPeriods: readonly string[] = ["week", "month", "year"] satisfies StatisticsPeriod[];

const statisticsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/statistics",
  // Неделя — по умолчанию и при неизвестном периоде в адресе.
  validateSearch: (search: Record<string, unknown>): { period?: StatisticsPeriod } =>
    typeof search.period === "string" && statisticsPeriods.includes(search.period) && search.period !== "week"
      ? { period: search.period as StatisticsPeriod }
      : {},
  component: StatisticsPage,
});

const devicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/devices",
  component: DevicesPage,
});

const passwordRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/password",
  component: ChangePasswordPage,
});

const helpRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/help",
  component: HelpIndexPage,
});

const helpProgressRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/help/progress",
  component: HelpProgressPage,
});

const helpStatisticsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/help/statistics",
  component: HelpStatisticsPage,
});

const helpTroubleshootingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/help/troubleshooting",
  component: HelpTroubleshootingPage,
});

const routeTree = rootRoute.addChildren([
  setupRoute,
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    booksRoute,
    bookRoute,
    statisticsRoute,
    devicesRoute,
    passwordRoute,
    helpRoute,
    helpProgressRoute,
    helpStatisticsRoute,
    helpTroubleshootingRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
