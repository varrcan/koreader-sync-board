import { SUPPORTED_LANGUAGES, isLanguage, setLanguage } from "@ksb/i18n";
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api";

function useVersion() {
  return useQuery({
    queryKey: ["version"],
    queryFn: async () => {
      const { data, error } = await api.GET("/api/v1/version");
      if (error) throw new Error(error.code);
      return data;
    },
  });
}

export function HomePage() {
  const { t, i18n } = useTranslation();
  const version = useVersion();

  useEffect(() => {
    document.documentElement.lang = i18n.language;
    document.title = t("app.title");
  }, [i18n.language, t]);

  return (
    <main className="page">
      <header className="page__header">
        <h1>{t("app.title")}</h1>
        <label className="lang">
          <span>{t("language.label")}</span>
          <select
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
        </label>
      </header>

      <section className="card">
        {version.isPending && <p className="muted">{t("version.loading")}</p>}
        {version.isError && <p className="error">{t("version.loadError")}</p>}
        {version.isSuccess && (
          <p>{t("version.label", { version: version.data.version, commit: version.data.commit })}</p>
        )}
      </section>
    </main>
  );
}
