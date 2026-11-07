import { useMutation, useQuery } from "@tanstack/react-query";
import { getRouteApi, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, ApiFailure, failure } from "@/api";
import { errorText, methodsQuery, safeRedirect, signedIn } from "@/auth";
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
  const { redirect, oidc_error: oidcError } = route.useSearch();
  const { data: methods } = useQuery(methodsQuery);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  // Вход по паролю только для непривязанных: форма свёрнута, главная — кнопка провайдера.
  const [showPassword, setShowPassword] = useState(methods?.password !== "unlinked" || !methods.oidc);

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

  let message: string | undefined;
  if (login.isError) message = errorText(t, login.error);
  else if (oidcError) message = errorText(t, new ApiFailure(oidcError));

  const oidc = methods?.oidc;
  const form = (
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
      <Button type="submit" variant={oidc ? "outline" : "default"} disabled={login.isPending}>
        {t("login.submit")}
      </Button>
    </form>
  );

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="font-serif text-2xl font-medium">{t("login.title")}</CardTitle>
          {showPassword && <CardDescription>{t("login.description")}</CardDescription>}
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <FormError message={message} />
          {oidc && (
            // Навигация, а не запрос: сервер уводит браузер на провайдера и обратно.
            <Button asChild>
              <a href={`/api/v1/auth/oidc/login?${new URLSearchParams({ redirect: safeRedirect(redirect) })}`}>
                {t("login.oidc", { name: oidc.name })}
              </a>
            </Button>
          )}
          {oidc && showPassword && (
            <div className="flex items-center gap-3 text-xs text-muted-foreground">
              <span className="h-px flex-1 bg-border" />
              {t("login.or")}
              <span className="h-px flex-1 bg-border" />
            </div>
          )}
          {showPassword ? (
            form
          ) : (
            <Button variant="link" className="self-center" onClick={() => setShowPassword(true)}>
              {t("login.usePassword")}
            </Button>
          )}
        </CardContent>
      </Card>
    </AuthShell>
  );
}
