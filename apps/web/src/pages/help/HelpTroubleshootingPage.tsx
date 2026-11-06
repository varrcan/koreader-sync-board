import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { HelpPage, HelpText, helpLink, ProblemItem } from "@/components/HelpParts";

const devices = <Link to="/devices" className={helpLink} />;

export function HelpTroubleshootingPage() {
  const { t } = useTranslation();
  return (
    <HelpPage title={t("help.pages.troubleshooting.title")} intro="help.troubleshooting.intro">
      <ProblemItem
        id="login-failed"
        problem="loginFailed"
        components={{ devices, a: <Link to="/help/progress" hash="setup" className={helpLink} /> }}
      >
        <HelpText k="help.troubleshooting.loginFailed.note" />
      </ProblemItem>
      <ProblemItem id="too-many-attempts" problem="tooManyAttempts" />
      <ProblemItem
        id="sync-rejected"
        problem="syncRejected"
        components={{
          devices,
          a: <Link to="/help/troubleshooting" hash="unknown-server-error" className={helpLink} />,
        }}
      />
      <ProblemItem
        id="no-progress"
        problem="noProgress"
        components={{ a: <Link to="/help/progress" hash="auto-sync" className={helpLink} /> }}
      />
      <ProblemItem
        id="unknown-book"
        problem="unknownBook"
        components={{
          stats: <Link to="/help/statistics" hash="setup" className={helpLink} />,
          a: <Link to="/help/progress" hash="metadata" className={helpLink} />,
        }}
      />
      <ProblemItem
        id="different-books"
        problem="differentBooks"
        components={{
          behavior: <Link to="/help/progress" hash="behavior" className={helpLink} />,
          formats: <Link to="/help/progress" hash="formats" className={helpLink} />,
          matching: <Link to="/help/progress" hash="matching" className={helpLink} />,
        }}
      />
      <ProblemItem
        id="unknown-server-error"
        problem="unknownServerError"
        components={{ a: <Link to="/help/troubleshooting" hash="sync-rejected" className={helpLink} /> }}
      />
      <ProblemItem
        id="stats-sync-disabled"
        problem="statsSyncDisabled"
        components={{ a: <Link to="/help/statistics" hash="setup" className={helpLink} /> }}
      />
      <ProblemItem
        id="stats-wrong-storage"
        problem="statsWrongStorage"
        components={{ a: <Link to="/help/statistics" hash="cloud-storage" className={helpLink} /> }}
      />
      <ProblemItem
        id="stats-no-titles"
        problem="statsNoTitles"
        components={{ a: <Link to="/help/progress" hash="matching" className={helpLink} /> }}
      />
    </HelpPage>
  );
}
