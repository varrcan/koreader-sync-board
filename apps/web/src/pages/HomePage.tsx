import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api, failure } from "@/api";
import { Card, CardContent } from "@/components/ui/card";

function useVersion() {
  return useQuery({
    queryKey: ["version"],
    queryFn: async () => {
      const { data, error, response } = await api.GET("/api/v1/version");
      if (error) throw failure(error, response);
      return data;
    },
  });
}

export function HomePage() {
  const { t } = useTranslation();
  const version = useVersion();

  return (
    <>
      <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("nav.home")}</h1>
      <Card className="max-w-xl py-4">
        <CardContent className="text-sm">
          {version.isPending && <p className="text-muted-foreground">{t("version.loading")}</p>}
          {version.isError && <p className="text-destructive">{t("version.loadError")}</p>}
          {version.isSuccess && (
            <p>{t("version.label", { version: version.data.version, commit: version.data.commit })}</p>
          )}
        </CardContent>
      </Card>
    </>
  );
}
