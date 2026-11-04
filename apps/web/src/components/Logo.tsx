import { BookOpen } from "lucide-react";
import { useTranslation } from "react-i18next";

export function Logo() {
  const { t } = useTranslation();
  return (
    <div className="flex items-center gap-2.5">
      <div className="flex size-6.5 items-center justify-center rounded-md bg-primary text-primary-foreground">
        <BookOpen className="size-4" strokeWidth={2.25} />
      </div>
      <span className="text-[15px] font-semibold tracking-tight">{t("app.brand")}</span>
    </div>
  );
}
