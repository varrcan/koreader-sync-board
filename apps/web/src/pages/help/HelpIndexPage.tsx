import { Link } from "@tanstack/react-router";
import { BookDown, ChartNoAxesColumn, ChevronRight, LifeBuoy, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

const pages = [
  { to: "/help/progress", key: "progress", icon: <RefreshCw /> },
  { to: "/help/statistics", key: "statistics", icon: <ChartNoAxesColumn /> },
  { to: "/help/opds", key: "opds", icon: <BookDown /> },
  { to: "/help/troubleshooting", key: "troubleshooting", icon: <LifeBuoy /> },
] as const;

export function HelpIndexPage() {
  const { t } = useTranslation();
  return (
    <>
      <header className="flex max-w-3xl flex-col gap-1.5">
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("nav.help")}</h1>
        <p className="text-sm text-pretty text-muted-foreground">{t("help.index.description")}</p>
      </header>
      <ul className="flex max-w-3xl flex-col gap-3">
        {pages.map((p) => (
          <li key={p.key}>
            <Link
              to={p.to}
              className="flex items-center gap-4 rounded-xl border bg-card px-5 py-4 hover:bg-accent [&_svg]:size-4.5"
            >
              <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-secondary text-muted-foreground">
                {p.icon}
              </div>
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="text-[15px] font-medium">{t(`help.pages.${p.key}.title`)}</span>
                <span className="text-sm text-pretty text-muted-foreground">{t(`help.pages.${p.key}.text`)}</span>
              </div>
              <ChevronRight className="shrink-0 text-muted-foreground" />
            </Link>
          </li>
        ))}
      </ul>
    </>
  );
}
