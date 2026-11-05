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
} as const;

export function MenuLabel({ item }: { item: keyof typeof koreaderMenu }) {
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
