import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { errorText } from "@/auth";
import { FormError } from "@/components/FormError";
import { Card } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { settingsQuery, useSaveSettings } from "@/settings";

export function SettingsPage() {
  const { t } = useTranslation();
  const settings = useQuery(settingsQuery);
  const save = useSaveSettings();
  const on = settings.data?.library_metadata_suggest ?? false;
  const progress = settings.data?.metadata_suggestions;

  return (
    <>
      <div className="flex max-w-3xl flex-col gap-1.5">
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("settings.title")}</h1>
        <p className="text-sm text-pretty text-muted-foreground">{t("settings.description")}</p>
      </div>

      {settings.isPending && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}
      {settings.isError && <p className="text-sm text-destructive">{t("common.loadError")}</p>}

      {settings.data && (
        <Card className="max-w-3xl gap-4 px-6">
          <div className="flex items-start justify-between gap-4">
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor="metadata-suggest" className="text-[15px] leading-5 font-semibold">
                {t("settings.metadataSuggest.label")}
              </Label>
              <p className="text-sm text-pretty text-muted-foreground">{t("settings.metadataSuggest.hint")}</p>
              <p className="text-sm text-pretty text-muted-foreground">{t("settings.metadataSuggest.privacy")}</p>
            </div>
            <Switch
              id="metadata-suggest"
              className="mt-0.5"
              checked={on}
              disabled={save.isPending}
              onCheckedChange={(checked) => save.mutate({ library_metadata_suggest: checked })}
            />
          </div>
          <FormError message={save.isError ? errorText(t, save.error) : undefined} />
          {on && progress && (
            <div className="flex flex-col gap-1 border-t pt-4 text-sm">
              <p className="tabular-nums">
                {progress.pending > 0
                  ? t("settings.metadataSuggest.pending", { count: progress.pending })
                  : t("settings.metadataSuggest.idle")}
              </p>
              <p className="tabular-nums">
                {progress.found > 0 ? (
                  <Link
                    to="/library"
                    search={{ suggested: 1 }}
                    className="font-medium underline-offset-2 hover:underline"
                  >
                    {t("settings.metadataSuggest.found", { count: progress.found })}
                  </Link>
                ) : (
                  <span className="text-muted-foreground">{t("settings.metadataSuggest.noneFound")}</span>
                )}
              </p>
            </div>
          )}
        </Card>
      )}
    </>
  );
}
