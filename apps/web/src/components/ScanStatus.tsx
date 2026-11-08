import type { LibraryScan } from "@ksb/api-client";
import { LoaderCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { formatRelative } from "@/lib/format";

export function ScanStatus({ scan, now }: { scan: LibraryScan; now: number }) {
  const { t, i18n } = useTranslation();
  return (
    <p className="flex items-center gap-1.5 text-sm text-muted-foreground [&_svg]:size-4">
      {scan.running ? (
        <>
          <LoaderCircle className="animate-spin" />
          {t("library.scanning")}
        </>
      ) : scan.finished_at ? (
        t("library.lastScan", { when: formatRelative(scan.finished_at, i18n.language, now) })
      ) : (
        t("library.neverScanned")
      )}
    </p>
  );
}
