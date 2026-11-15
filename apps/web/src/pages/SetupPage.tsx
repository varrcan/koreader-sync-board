import { useMutation } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, ApiFailure, failure } from "@/api";
import { errorText, queryClient, setupQuery, signedIn } from "@/auth";
import { AuthShell } from "@/components/AuthShell";
import { CopyButton } from "@/components/CopyButton";
import { FormError } from "@/components/FormError";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

// docs/installation.md запускает контейнер с --name ksb, а бинарник — как ksb.service.
const commands = [
  { key: "docker", command: "docker logs ksb 2>&1 | grep setup_code" },
  { key: "systemd", command: "journalctl -u ksb | grep setup_code" },
] as const;

export function SetupPage() {
  const { t } = useTranslation();
  const router = useRouter();
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const setup = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST("/api/v1/setup", { body: { code, username, password } });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (me) => {
      signedIn(me);
      router.history.push("/");
    },
    onError: (err) => {
      if (err instanceof ApiFailure && err.code === "setup_completed") {
        queryClient.setQueryData(setupQuery.queryKey, { required: false });
      }
    },
  });
  const completed = setup.error instanceof ApiFailure && setup.error.code === "setup_completed";

  return (
    <AuthShell>
      <Card className="w-full max-w-lg">
        <CardHeader>
          <CardTitle className="font-serif text-2xl font-medium">{t("setup.title")}</CardTitle>
          <CardDescription className="text-pretty">{t("setup.description")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-6">
          <section className="flex flex-col gap-3">
            <h2 className="text-xs font-medium tracking-wider text-muted-foreground uppercase">{t("setup.findCode")}</h2>
            {commands.map(({ key, command }) => (
              <div key={key} className="flex flex-col gap-1.5">
                <span className="text-sm text-muted-foreground">{t(`setup.${key}`)}</span>
                <div className="flex min-w-0 items-center justify-between gap-3 rounded-lg bg-muted py-2 pr-2 pl-3">
                  <code className="overflow-x-auto font-mono text-[13px] whitespace-nowrap">{command}</code>
                  <CopyButton text={command} />
                </div>
              </div>
            ))}
            <p className="text-sm text-muted-foreground">{t("setup.restartHint")}</p>
          </section>

          <form
            className="flex flex-col gap-4 border-t pt-6"
            onSubmit={(e) => {
              e.preventDefault();
              setup.mutate();
            }}
          >
            <div className="flex flex-col gap-2">
              <Label htmlFor="code">{t("setup.code")}</Label>
              <Input
                id="code"
                className="font-mono"
                placeholder="xxxx-xxxx-xxxx"
                autoComplete="one-time-code"
                autoCapitalize="none"
                spellCheck={false}
                required
                autoFocus
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="username">{t("auth.username")}</Label>
              <Input
                id="username"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="password">{t("auth.password")}</Label>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={8}
                maxLength={256}
                aria-describedby="password-hint"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              <p id="password-hint" className="text-sm text-muted-foreground">
                {t("setup.passwordHint")}
              </p>
            </div>
            <FormError message={setup.isError ? errorText(t, setup.error) : undefined} />
            {completed ? (
              <Button asChild>
                <Link to="/login">{t("setup.toLogin")}</Link>
              </Button>
            ) : (
              <Button type="submit" disabled={setup.isPending}>
                {t("setup.submit")}
              </Button>
            )}
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
