import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/auth";
import { HelpPage, HelpSection, HelpText, helpLink } from "@/components/HelpParts";
import { WebdavSteps } from "@/components/KoreaderSetup";

export function HelpStatisticsPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  return (
    <HelpPage title={t("help.pages.statistics.title")} intro="help.statistics.intro">
      <HelpSection id="what" title={t("help.statistics.what.title")}>
        <HelpText k="help.statistics.what.text" />
        <HelpText k="help.statistics.what.vocabulary" />
      </HelpSection>

      <HelpSection id="setup" title={t("help.statistics.setup.title")}>
        <HelpText
          k="help.statistics.setup.version"
          components={{ a: <Link to="/help/statistics" hash="cloud-storage" className={helpLink} /> }}
        />
        <WebdavSteps credentials={t("help.statistics.setup.credentials", { username: me?.username ?? "" })} />
      </HelpSection>

      <HelpSection id="cloud-storage" title={t("help.statistics.cloudStorage.title")}>
        <HelpText k="help.statistics.cloudStorage.text" />
        <HelpText k="help.statistics.cloudStorage.old" />
      </HelpSection>

      <HelpSection id="sync" title={t("help.statistics.sync.title")}>
        <HelpText k="help.statistics.sync.text" />
        <HelpText k="help.statistics.sync.disabled" />
      </HelpSection>

      <HelpSection id="vocabulary" title={t("help.statistics.vocabulary.title")}>
        <HelpText k="help.statistics.vocabulary.text" />
      </HelpSection>

      <HelpSection id="upload" title={t("help.statistics.upload.title")}>
        <HelpText
          k="help.statistics.upload.text"
          components={{ a: <Link to="/devices" className={helpLink} /> }}
        />
      </HelpSection>

      <HelpSection id="folders" title={t("help.statistics.folders.title")}>
        <HelpText k="help.statistics.folders.text" />
      </HelpSection>

      <HelpSection id="numbers" title={t("help.statistics.numbers.title")}>
        <HelpText k="help.statistics.numbers.days" />
        <HelpText k="help.statistics.numbers.pages" />
        <HelpText k="help.statistics.numbers.sessions" />
        <HelpText k="help.statistics.numbers.noData" />
        <HelpText k="help.statistics.numbers.book" />
      </HelpSection>
    </HelpPage>
  );
}
