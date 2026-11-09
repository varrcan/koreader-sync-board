import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/auth";
import { HelpPage, HelpSection, HelpText, helpLink } from "@/components/HelpParts";
import { opdsAddress, Value } from "@/components/KoreaderSetup";

export function HelpOpdsPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  return (
    <HelpPage title={t("help.pages.opds.title")} intro="help.opds.intro">
      <HelpSection id="setup" title={t("help.opds.setup.title")}>
        <HelpText k="help.opds.setup.open" />
        <HelpText k="help.opds.setup.name" />
        <Value label={t("help.opds.setup.address")} value={opdsAddress()} />
        <Value label={t("devices.koreader.username")} value={me?.username ?? ""} />
        <HelpText k="help.opds.setup.password" components={{ a: <Link to="/devices" className={helpLink} /> }} />
      </HelpSection>

      <HelpSection id="epub" title={t("help.opds.epub.title")}>
        <HelpText k="help.opds.epub.text" />
        <HelpText k="help.opds.epub.timeout" />
      </HelpSection>

      <HelpSection id="filenames" title={t("help.opds.filenames.title")}>
        <HelpText k="help.opds.filenames.text" />
      </HelpSection>
    </HelpPage>
  );
}
