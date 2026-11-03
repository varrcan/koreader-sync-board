// Инициализация i18next для веб-панели.
import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import en from "./locales/en.json";
import ru from "./locales/ru.json";

// Типизация ключей перевода: эталон — русский каталог, поэтому
// t("несуществующий.ключ") даёт ошибку компиляции. Объявлено здесь, а не в
// отдельном .d.ts, чтобы augmentation подхватывался всеми импортёрами пакета.
declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "translation";
    resources: {
      translation: typeof ru;
    };
  }
}

export const SUPPORTED_LANGUAGES = ["ru", "en"] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];

const FALLBACK_LANGUAGE: Language = "en";
const STORAGE_KEY = "ksb.language";

function isLanguage(value: unknown): value is Language {
  return typeof value === "string" && (SUPPORTED_LANGUAGES as readonly string[]).includes(value);
}

// localStorage может быть недоступен (приватный режим, запрет cookies) — отсюда try/catch.
function readStoredLanguage(): Language | undefined {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return isLanguage(stored) ? stored : undefined;
  } catch {
    return undefined;
  }
}

function detectLanguage(): Language {
  const stored = readStoredLanguage();
  if (stored) return stored;
  const browser = typeof navigator !== "undefined" ? navigator.language.slice(0, 2).toLowerCase() : "";
  return isLanguage(browser) ? browser : FALLBACK_LANGUAGE;
}

/** Переключает язык и запоминает выбор. */
export async function setLanguage(lang: Language): Promise<void> {
  try {
    localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    // Не сохранили — язык всё равно сменится до перезагрузки.
  }
  await i18n.changeLanguage(lang);
}

/** Инициализирует i18next; вызывать один раз до рендера. */
export function initI18n() {
  void i18n.use(initReactI18next).init({
    resources: {
      ru: { translation: ru },
      en: { translation: en },
    },
    lng: detectLanguage(),
    fallbackLng: FALLBACK_LANGUAGE,
    supportedLngs: [...SUPPORTED_LANGUAGES],
    interpolation: { escapeValue: false },
  });
  return i18n;
}

export { i18n, isLanguage };
