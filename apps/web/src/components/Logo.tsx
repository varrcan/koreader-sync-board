import { BookOpen } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

/** compact — в мобильной шапке: на самых узких экранах название не помещается рядом с кнопками. */
export function Logo({ compact }: { compact?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className="flex items-center gap-2.5">
      <div className="flex size-6.5 items-center justify-center rounded-md bg-primary text-primary-foreground">
        <BookOpen className="size-4" strokeWidth={2.25} />
      </div>
      <span
        className={cn("text-[15px] font-semibold tracking-tight", compact && "whitespace-nowrap max-[400px]:sr-only")}
      >
        {t("app.brand")}
      </span>
    </div>
  );
}
