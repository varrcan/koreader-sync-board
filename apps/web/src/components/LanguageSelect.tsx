import { isLanguage, setLanguage, SUPPORTED_LANGUAGES } from "@ksb/i18n";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

export function LanguageSelect({ className }: { className?: string }) {
  const { t, i18n } = useTranslation();
  return (
    <select
      aria-label={t("language.label")}
      className={cn(
        "h-8 rounded-lg border border-input bg-transparent px-2 text-sm text-muted-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30",
        className,
      )}
      value={i18n.language}
      onChange={(e) => {
        if (isLanguage(e.target.value)) void setLanguage(e.target.value);
      }}
    >
      {SUPPORTED_LANGUAGES.map((lng) => (
        <option key={lng} value={lng}>
          {lng.toUpperCase()}
        </option>
      ))}
    </select>
  );
}
