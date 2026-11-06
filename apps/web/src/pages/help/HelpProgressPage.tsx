import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/auth";
import { HelpPage, HelpSection, HelpText, helpLink } from "@/components/HelpParts";
import { Value } from "@/components/KoreaderSetup";

export function HelpProgressPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  return (
    <HelpPage title={t("help.pages.progress.title")} intro="help.progress.intro">
      <HelpSection id="setup" title={t("help.progress.setup.title")}>
        <HelpText
          k="help.progress.setup.device"
          components={{ a: <Link to="/devices" className={helpLink} /> }}
        />
        <HelpText k="help.progress.setup.server" />
        <Value label={t("devices.koreader.server")} value={window.location.origin} />
        <p className="text-sm text-pretty">{t("devices.koreader.serverHint")}</p>
        <HelpText k="help.progress.setup.login" />
        {me && <Value label={t("devices.koreader.username")} value={me.username} />}
        <p className="text-sm text-pretty">{t("help.progress.setup.password")}</p>
        <HelpText
          k="help.progress.setup.metadata"
          components={{ a: <Link to="/help/progress" hash="metadata" className={helpLink} /> }}
        />
      </HelpSection>

      <HelpSection id="auto-sync" title={t("help.progress.autoSync.title")}>
        <HelpText k="help.progress.autoSync.default" />
        <HelpText k="help.progress.autoSync.enabled" />
        <HelpText k="help.progress.autoSync.pages" />
        <HelpText k="help.progress.autoSync.wifi" />
        <HelpText k="help.progress.autoSync.logout" />
      </HelpSection>

      <HelpSection id="behavior" title={t("help.progress.behavior.title")}>
        <HelpText k="help.progress.behavior.intro" />
        <HelpText k="help.progress.behavior.time" />
        <ul className="flex list-disc flex-col gap-2 pl-5">
          <li>
            <HelpText k="help.progress.behavior.newer" />
          </li>
          <li>
            <HelpText k="help.progress.behavior.older" />
          </li>
        </ul>
        <HelpText k="help.progress.behavior.pull" />
      </HelpSection>

      <HelpSection id="metadata" title={t("help.progress.metadata.title")}>
        <HelpText
          k="help.progress.metadata.text"
          components={{ a: <Link to="/help/statistics" className={helpLink} /> }}
        />
      </HelpSection>

      <HelpSection id="matching" title={t("help.progress.matching.title")}>
        <HelpText k="help.progress.matching.intro" />
        <ul className="flex list-disc flex-col gap-2 pl-5">
          <li>
            <HelpText k="help.progress.matching.binary" />
          </li>
          <li>
            <HelpText k="help.progress.matching.filename" />
          </li>
        </ul>
        <HelpText k="help.progress.matching.same" />
      </HelpSection>

      <HelpSection id="formats" title={t("help.progress.formats.title")}>
        <HelpText k="help.progress.formats.text" />
      </HelpSection>
    </HelpPage>
  );
}
