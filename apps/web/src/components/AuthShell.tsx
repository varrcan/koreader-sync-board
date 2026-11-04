import type { ReactNode } from "react";
import { LanguageSelect } from "./LanguageSelect";
import { Logo } from "./Logo";

/** Экран без сессии (вход, мастер): логотип, язык и карточка по центру. */
export function AuthShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-svh flex-col">
      <header className="flex items-center justify-between px-4 py-4 sm:px-8">
        <Logo />
        <LanguageSelect />
      </header>
      <main className="flex flex-1 items-start justify-center px-4 pt-6 pb-16 sm:items-center sm:pt-0">{children}</main>
    </div>
  );
}
