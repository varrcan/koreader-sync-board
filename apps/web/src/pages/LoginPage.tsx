import { useMutation } from "@tanstack/react-query";
import { getRouteApi, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, failure } from "@/api";
import { errorText, safeRedirect, signedIn } from "@/auth";
import { AuthShell } from "@/components/AuthShell";
import { FormError } from "@/components/FormError";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const route = getRouteApi("/login");

export function LoginPage() {
  const { t } = useTranslation();
  const router = useRouter();
  const { redirect } = route.useSearch();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const login = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST("/api/v1/auth/login", { body: { username, password } });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (me) => {
      signedIn(me);
      router.history.push(safeRedirect(redirect));
    },
  });

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="font-serif text-2xl font-medium">{t("login.title")}</CardTitle>
          <CardDescription>{t("login.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              login.mutate();
            }}
          >
            <div className="flex flex-col gap-2">
              <Label htmlFor="username">{t("auth.username")}</Label>
              <Input
                id="username"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                required
                autoFocus
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="password">{t("auth.password")}</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <FormError message={login.isError ? errorText(t, login.error) : undefined} />
            <Button type="submit" disabled={login.isPending}>
              {t("login.submit")}
            </Button>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
