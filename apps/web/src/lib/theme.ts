import { useSyncExternalStore } from "react";

export type Theme = "dark" | "light";

// Этот же ключ до первой отрисовки читает скрипт в index.html.
const STORAGE_KEY = "ksb.theme";

const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function current(): Theme {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

/** Переключает тему и запоминает выбор. */
export function setTheme(theme: Theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // Не сохранили — тема всё равно сменится до перезагрузки.
  }
  listeners.forEach((listener) => listener());
}

export function useTheme() {
  return useSyncExternalStore(subscribe, current);
}
