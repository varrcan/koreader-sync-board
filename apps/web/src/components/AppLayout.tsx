import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, Outlet, useRouter } from "@tanstack/react-router";
import { BookOpen, CircleHelp, House, KeyRound, LogOut, TabletSmartphone } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { api } from "@/api";
import { meQuery, signedOut } from "@/auth";
import { Button } from "@/components/ui/button";
import { LanguageSelect } from "./LanguageSelect";
import { Logo } from "./Logo";

function NavLink({
  to,
  icon,
  children,
}: {
  to: "/" | "/books" | "/devices" | "/password" | "/help";
  icon: ReactNode;
  children: ReactNode;
}) {
  return (
    <Link
      to={to}
      className="flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm text-muted-foreground hover:bg-accent hover:text-foreground [&_svg]:size-4.5"
      activeProps={{ className: "bg-accent font-medium text-foreground" }}
      // «Книги» подсвечены и в карточке книги, «Справка» — на её страницах.
      activeOptions={{ exact: to === "/", includeSearch: false }}
    >
      {icon}
      {children}
    </Link>
  );
}

/** Каркас страниц после входа; сайдбар и блок пользователя — по HomeScreen. */
export function AppLayout() {
  const { t } = useTranslation();
  const router = useRouter();
  const { data: me } = useQuery(meQuery);

  const logout = useMutation({
    mutationFn: () => api.POST("/api/v1/auth/logout"),
    onSettled: () => {
      signedOut();
      router.history.push("/login");
    },
  });

  const user = me && (
    <div className="flex min-w-0 items-center gap-2.5">
      <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-secondary text-[13px] font-semibold uppercase">
        {me.username.slice(0, 1)}
      </div>
      <div className="flex min-w-0 flex-col">
        <span className="truncate text-[13px] font-medium">{me.username}</span>
        <span className="text-xs text-muted-foreground">{t(`roles.${me.role}`)}</span>
      </div>
    </div>
  );
  const logoutButton = (
    <Button
      variant="ghost"
      size="icon-sm"
      title={t("auth.logout")}
      aria-label={t("auth.logout")}
      disabled={logout.isPending}
      onClick={() => logout.mutate()}
    >
      <LogOut />
    </Button>
  );

  return (
    // Меню и колонка содержимого — один блок по центру: на широком мониторе
    // пустое поле делится поровну, а не остаётся справа. Ширина — меню (w-58),
    // самая широкая колонка страниц (max-w-5xl) и отступы main (md:px-8).
    <div className="mx-auto flex min-h-svh w-full max-w-[calc(--spacing(58)+var(--container-5xl)+--spacing(16))]">
      <aside className="sticky top-0 hidden h-svh w-58 shrink-0 flex-col gap-7 border-r px-3 py-5 md:flex">
        <div className="px-2">
          <Logo />
        </div>
        <nav className="flex flex-col gap-0.5">
          <NavLink to="/" icon={<House />}>
            {t("nav.home")}
          </NavLink>
          <NavLink to="/books" icon={<BookOpen />}>
            {t("nav.books")}
          </NavLink>
          <NavLink to="/devices" icon={<TabletSmartphone />}>
            {t("nav.devices")}
          </NavLink>
          <NavLink to="/password" icon={<KeyRound />}>
            {t("nav.password")}
          </NavLink>
          <NavLink to="/help" icon={<CircleHelp />}>
            {t("nav.help")}
          </NavLink>
        </nav>
        <div className="mt-auto flex flex-col gap-3 border-t px-2 pt-4">
          <div className="flex items-center justify-between gap-2">
            {user}
            {logoutButton}
          </div>
          <LanguageSelect className="self-start" />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-between gap-3 border-b bg-sidebar px-4 py-3 md:hidden">
          <Link to="/">
            <Logo compact />
          </Link>
          <div className="flex items-center gap-1">
            <LanguageSelect />
            <Button variant="ghost" size="icon-sm" asChild title={t("nav.books")} aria-label={t("nav.books")}>
              <Link
                to="/books"
                activeOptions={{ includeSearch: false }}
                activeProps={{ className: "bg-accent text-foreground" }}
              >
                <BookOpen />
              </Link>
            </Button>
            <Button variant="ghost" size="icon-sm" asChild title={t("nav.devices")} aria-label={t("nav.devices")}>
              <Link to="/devices" activeProps={{ className: "bg-accent text-foreground" }}>
                <TabletSmartphone />
              </Link>
            </Button>
            <Button variant="ghost" size="icon-sm" asChild title={t("nav.password")} aria-label={t("nav.password")}>
              <Link to="/password">
                <KeyRound />
              </Link>
            </Button>
            <Button variant="ghost" size="icon-sm" asChild title={t("nav.help")} aria-label={t("nav.help")}>
              <Link to="/help" activeProps={{ className: "bg-accent text-foreground" }}>
                <CircleHelp />
              </Link>
            </Button>
            {logoutButton}
          </div>
        </header>
        <main className="flex flex-col gap-7 px-4 py-5 md:px-8 md:py-7">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
