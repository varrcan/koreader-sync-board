// Code-based routing TanStack Router, без генератора маршрутов.
import { createRootRoute, createRoute, createRouter, Outlet, redirect } from "@tanstack/react-router";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, methodsQuery, queryClient, safeRedirect, setupQuery } from "./auth";
import type { BookFilter, StatisticsPeriod } from "@ksb/api-client";
import { AppLayout } from "./components/AppLayout";
import { BookPage } from "./pages/BookPage";
import { BooksPage } from "./pages/BooksPage";
import { AccountPage } from "./pages/AccountPage";
import { DevicesPage } from "./pages/DevicesPage";
import { HelpIndexPage } from "./pages/help/HelpIndexPage";
import { HelpOpdsPage } from "./pages/help/HelpOpdsPage";
import { HelpProgressPage } from "./pages/help/HelpProgressPage";
import { HelpStatisticsPage } from "./pages/help/HelpStatisticsPage";
import { HelpTroubleshootingPage } from "./pages/help/HelpTroubleshootingPage";
import { HomePage } from "./pages/HomePage";
import { LibraryBookPage } from "./pages/LibraryBookPage";
import { LibraryPage } from "./pages/LibraryPage";
import { LibraryRootsPage } from "./pages/LibraryRootsPage";
import { LoginPage } from "./pages/LoginPage";
import { SettingsPage } from "./pages/SettingsPage";
import { SetupPage } from "./pages/SetupPage";
import { StatisticsPage } from "./pages/StatisticsPage";
import { UsersPage } from "./pages/UsersPage";

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
  // oidc_error — код неудачного OIDC-входа: сервер возвращает браузер сюда.
  validateSearch: (search: Record<string, unknown>): { redirect?: string; oidc_error?: string } => ({
    ...(typeof search.redirect === "string" ? { redirect: search.redirect } : {}),
    ...(typeof search.oidc_error === "string" ? { oidc_error: search.oidc_error } : {}),
  }),
  beforeLoad: async ({ search }) => {
    const setup = await queryClient.ensureQueryData(setupQuery);
    if (setup.required) throw redirect({ to: "/setup" });
    if (await queryClient.ensureQueryData(meQuery)) throw redirect({ href: safeRedirect(search.redirect) });
    await queryClient.ensureQueryData(methodsQuery);
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

const libraryRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/library",
  // suggested=1 — только книги с предложением фонового поиска (фильтр администратора).
  validateSearch: (search: Record<string, unknown>): { q?: string; suggested?: 1 } => ({
    ...(typeof search.q === "string" && search.q !== "" ? { q: search.q } : {}),
    ...(search.suggested === 1 || search.suggested === "1" || search.suggested === true ? { suggested: 1 as const } : {}),
  }),
  component: LibraryPage,
});

const libraryRootsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/library/roots",
  beforeLoad: async () => {
    const me = await queryClient.ensureQueryData(meQuery);
    if (me?.role !== "admin") throw redirect({ to: "/library", replace: true });
  },
  component: LibraryRootsPage,
});

const libraryBookRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/library/books/$id",
  component: LibraryBookPage,
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

// Не /users: этот префикс принадлежит kosync, и обновление страницы ушло бы в протокол.
const usersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/admin/users",
  beforeLoad: async () => {
    const me = await queryClient.ensureQueryData(meQuery);
    if (me?.role !== "admin") throw redirect({ to: "/", replace: true });
    await queryClient.ensureQueryData(methodsQuery);
  },
  component: UsersPage,
});

const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/admin/settings",
  beforeLoad: async () => {
    const me = await queryClient.ensureQueryData(meQuery);
    if (me?.role !== "admin") throw redirect({ to: "/", replace: true });
  },
  component: SettingsPage,
});

// Результат привязки OIDC: сервер возвращает браузер сюда с oidc=linked или oidc_error.
const accountRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/account",
  validateSearch: (search: Record<string, unknown>): { oidc?: "linked"; oidc_error?: string } => ({
    ...(search.oidc === "linked" ? { oidc: "linked" as const } : {}),
    ...(typeof search.oidc_error === "string" ? { oidc_error: search.oidc_error } : {}),
  }),
  beforeLoad: async () => {
    await queryClient.ensureQueryData(methodsQuery);
  },
  component: AccountPage,
});

// Старый адрес «Сменить пароль».
const passwordRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/password",
  beforeLoad: () => {
    throw redirect({ to: "/account", replace: true });
  },
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

const helpOpdsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/help/opds",
  component: HelpOpdsPage,
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
    libraryRoute,
    libraryRootsRoute,
    libraryBookRoute,
    statisticsRoute,
    devicesRoute,
    usersRoute,
    settingsRoute,
    accountRoute,
    passwordRoute,
    helpRoute,
    helpProgressRoute,
    helpStatisticsRoute,
    helpOpdsRoute,
    helpTroubleshootingRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
