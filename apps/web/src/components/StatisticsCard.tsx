import type { StatisticsSnapshot } from "@ksb/api-client";
import { useQuery } from "@tanstack/react-query";
import { ChartNoAxesColumn, Upload } from "lucide-react";
import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { errorText, meQuery } from "@/auth";
import { FormError } from "@/components/FormError";
import { WebdavSteps } from "@/components/KoreaderSetup";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { formatDate, formatRelative } from "@/lib/format";
import { statisticsQuery, useImportStatistics } from "@/statistics";

function SnapshotState({ snapshot, now }: { snapshot: StatisticsSnapshot; now: number }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const when = formatRelative(snapshot.uploaded_at, lang, now);
  return (
    <div className="flex flex-col gap-0.5 text-sm">
      <p className="font-medium">
        {snapshot.device_name
          ? t("statistics.fromDevice", { when, device: snapshot.device_name })
          : t("statistics.fromImport", { when })}
      </p>
      <p className="text-muted-foreground">
        {t("statistics.summary", { books: snapshot.books, records: snapshot.records })}
        {snapshot.first_read_at && snapshot.last_read_at && (
          <>
            {" · "}
            {t("statistics.period", {
              from: formatDate(snapshot.first_read_at, lang),
              to: formatDate(snapshot.last_read_at, lang),
            })}
          </>
        )}
      </p>
    </div>
  );
}

/** Карточка «Статистика чтения»: состояние снимка, адрес WebDAV и загрузка файла. */
export function StatisticsCard() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  const state = useQuery(statisticsQuery);
  const upload = useImportStatistics();
  const input = useRef<HTMLInputElement>(null);
  const snapshot = state.data?.snapshot;

  return (
    <Card className="max-w-3xl gap-4 px-6">
      <div className="flex items-start gap-3">
        <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-secondary text-muted-foreground [&_svg]:size-4.5">
          <ChartNoAxesColumn />
        </div>
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h2 className="text-base font-medium">{t("statistics.title")}</h2>
          <p className="text-sm text-pretty text-muted-foreground">{t("statistics.description")}</p>
        </div>
      </div>

      {state.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {state.isError && <p className="text-sm text-destructive">{t("statistics.loadError")}</p>}
      {state.isSuccess &&
        (snapshot ? (
          <SnapshotState snapshot={snapshot} now={state.dataUpdatedAt} />
        ) : (
          <p className="text-sm text-muted-foreground">{t("statistics.never")}</p>
        ))}

      <details>
        <summary className="text-sm font-medium select-none hover:text-foreground">
          {t("statistics.setupTitle")}
        </summary>
        <div className="pt-4">
          <WebdavSteps credentials={t("statistics.credentials", { username: me?.username ?? "" })} />
        </div>
      </details>

      <div className="flex flex-col items-start gap-2 border-t pt-4">
        <input
          ref={input}
          type="file"
          accept=".sqlite3"
          className="hidden"
          onChange={(e) => {
            const file = e.target.files?.[0];
            // Сброс, чтобы повторный выбор того же файла тоже срабатывал.
            e.target.value = "";
            if (file) upload.mutate(file);
          }}
        />
        <Button variant="outline" disabled={upload.isPending} onClick={() => input.current?.click()}>
          <Upload />
          {upload.isPending ? t("statistics.uploading") : t("statistics.upload")}
        </Button>
        <p className="text-xs text-pretty text-muted-foreground">{t("statistics.uploadHint")}</p>
        {upload.isSuccess && <p className="text-sm text-muted-foreground">{t("statistics.uploaded")}</p>}
        <FormError message={upload.isError ? errorText(t, upload.error) : undefined} />
      </div>
    </Card>
  );
}
