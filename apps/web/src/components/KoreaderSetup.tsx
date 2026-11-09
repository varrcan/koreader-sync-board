import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CopyButton } from "@/components/CopyButton";
import { cn } from "@/lib/utils";

// Подписи меню KOReader. Пользователь ищет их в меню читалки на её языке,
// поэтому рядом с переводом из локали KOReader всегда стоит английский оригинал.
const koreaderMenu = {
  progressSync: "Progress sync",
  customServer: "Custom sync server",
  registerLogin: "Register / Login",
  login: "Login",
  sendMetadata: "Send document metadata",
  cloudStorage: "Cloud storage",
  readingStatistics: "Reading statistics",
  settings: "Settings",
  cloudSync: "Cloud sync",
  chooseFolder: "Long-press here to choose current folder",
  syncNow: "Sync now",
  autoSync: "Automatically keep documents in sync",
  // Строки с %1 в KOReader показываются без подставленного значения.
  syncEveryPages: "Periodically sync every # pages",
  syncBehavior: "Sync behavior",
  syncNewer: "Sync to a newer state",
  syncOlder: "Sync to an older state",
  pushNow: "Push progress from this device now",
  pullNow: "Pull progress from other devices now",
  matchingMethod: "Document matching method",
  matchingBinary: "Binary. Only identical files will be kept in sync.",
  matchingFilename: "Filename. Files with matching names will be kept in sync.",
  network: "Network",
  wifiAction: "Action when Wi-Fi is off",
  turnOn: "Turn on",
  cloudStoragePlus: "Cloud storage+",
  vocabularyBuilder: "Vocabulary builder",
  logout: "Logout",
  search: "Search",
  opdsCatalog: "OPDS catalog",
  addCatalog: "Add catalog",
  catalogName: "Catalog name",
  catalogUrl: "Catalog URL",
  catalogUsername: "Username (optional)",
  catalogPassword: "Password (optional)",
  serverFilenames: "Use server filenames",
} as const;

export type MenuItem = keyof typeof koreaderMenu;

/** Адрес WebDAV для облачной синхронизации KOReader: тот же origin, что у панели. */
export function webdavAddress(): string {
  return `${window.location.origin}/dav`;
}

/** Адрес каталога OPDS: тот же origin, что у панели. */
export function opdsAddress(): string {
  return `${window.location.origin}/opds`;
}

export function MenuLabel({ item }: { item: MenuItem }) {
  const { t } = useTranslation();
  const local = t(`devices.koreader.menu.${item}`);
  const original = koreaderMenu[item];
  return (
    <span className="font-medium text-foreground">
      {t("devices.koreader.quote", { label: local })}
      {local !== original && <span className="font-normal text-muted-foreground"> ({original})</span>}
    </span>
  );
}

/** Пункты меню для Trans: в тексте перевода пишется <autoSync/>. */
export const menuComponents = Object.fromEntries(
  (Object.keys(koreaderMenu) as MenuItem[]).map((item) => [item, <MenuLabel item={item} />]),
);

/** Значение, которое переписывают в читалку, с кнопкой копирования. */
export function Value({ label, value, large }: { label?: string; value: string; large?: boolean }) {
  return (
    <div className="flex flex-col gap-1.5">
      {label && <span className="text-xs text-muted-foreground">{label}</span>}
      <div className="flex min-w-0 items-center justify-between gap-3 rounded-lg bg-muted py-2 pr-2 pl-3">
        <code
          className={cn(
            "overflow-x-auto font-mono whitespace-nowrap",
            large ? "text-lg tracking-wider" : "text-[13px]",
          )}
        >
          {value}
        </code>
        <CopyButton text={value} />
      </div>
    </div>
  );
}

export function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-secondary text-xs font-semibold">
        {n}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-3 pt-0.5 text-sm text-muted-foreground">{children}</div>
    </li>
  );
}

/**
 * Настройка облачной синхронизации статистики в KOReader. credentials — откуда
 * взять имя и пароль: в карточке пароля они показаны выше, на экране устройств — нет.
 */
export function WebdavSteps({ credentials }: { credentials: string }) {
  const { t } = useTranslation();
  return (
    <ol className="flex flex-col gap-4">
      <Step n={1}>
        <p>
          {t("devices.webdav.step1")} <MenuLabel item="cloudStorage" /> {t("devices.webdav.step1Hint")}
        </p>
        <p className="text-xs">{t("devices.webdav.step1Legacy")}</p>
        <Value label={t("devices.webdav.address")} value={webdavAddress()} />
        <p className="text-xs">{credentials}</p>
      </Step>
      <Step n={2}>
        <p>
          {t("devices.webdav.step2")} <MenuLabel item="readingStatistics" /> → <MenuLabel item="settings" /> →{" "}
          <MenuLabel item="cloudSync" /> {t("devices.webdav.step2Hint")} <MenuLabel item="chooseFolder" />
        </p>
      </Step>
      <Step n={3}>
        <p>
          {t("devices.webdav.step3")} <MenuLabel item="readingStatistics" /> → <MenuLabel item="syncNow" />
        </p>
        <p className="text-xs">{t("devices.webdav.step3Hint")}</p>
      </Step>
    </ol>
  );
}
