import type { Me } from "@ksb/api-client";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getRouteApi } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, ApiFailure, failure } from "@/api";
import { errorText, meQuery, methodsQuery, queryClient } from "@/auth";
import { FormError } from "@/components/FormError";
import { ThemeToggle } from "@/components/ThemeToggle";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

const route = getRouteApi("/app/account");

function updateMe(patch: Partial<Me>) {
  queryClient.setQueryData(meQuery.queryKey, (me) => (me ? { ...me, ...patch } : me));
}

export function AccountPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  const { data: methods } = useQuery(methodsQuery);
  if (!me || !methods) return null;
  // При выключенном входе по паролю пароль привязанного пользователя не работает.
  const showPassword = !(methods.password === "unlinked" && me.oidc_linked);

  return (
    <>
      <div className="flex max-w-md flex-col gap-1.5">
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("account.title")}</h1>
        <p className="text-sm text-pretty text-muted-foreground">{t("account.description")}</p>
      </div>
      {/* В мобильной шапке места нет: на телефоне тема переключается здесь. */}
      <div className="flex w-full max-w-md items-center justify-between gap-2 md:hidden">
        <span className="text-sm">{t("theme.label")}</span>
        <ThemeToggle />
      </div>
      {showPassword && <PasswordCard me={me} />}
      {methods.oidc && (
        <OIDCCard me={me} name={methods.oidc.name} origin={methods.oidc.origin} canSetPassword={showPassword} />
      )}
    </>
  );
}

function PasswordCard({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [done, setDone] = useState<string>();
  // Первый пароль (пользователь создан входом через провайдера) задаётся без текущего.
  const first = !me.has_password;

  const change = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.PUT("/api/v1/me/password", {
        body: first ? { new_password: next } : { current_password: current, new_password: next },
      });
      if (error) throw failure(error, response);
    },
    onMutate: () => setDone(undefined),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      // После первого пароля карточка переключается на смену: текст запоминаем заранее.
      setDone(first ? t("password.set") : t("password.changed"));
      if (first) updateMe({ has_password: true });
    },
  });
  // Здесь invalid_credentials — неверный текущий пароль, а не логин.
  let message: string | undefined;
  if (change.error instanceof ApiFailure && change.error.code === "invalid_credentials") {
    message = t("errors.invalid_current_password");
  } else if (change.isError) {
    message = errorText(t, change.error);
  }

  return (
    <Card className="w-full max-w-md">
      <CardHeader>
        <CardTitle className="text-lg">{first ? t("password.setTitle") : t("password.title")}</CardTitle>
        <CardDescription>{first ? t("password.setDescription") : t("password.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            change.mutate();
          }}
        >
          {!first && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="current-password">{t("password.current")}</Label>
              <Input
                id="current-password"
                type="password"
                autoComplete="current-password"
                required
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            </div>
          )}
          <div className="flex flex-col gap-2">
            <Label htmlFor="new-password">{t("password.new")}</Label>
            <Input
              id="new-password"
              type="password"
              autoComplete="new-password"
              required
              minLength={8}
              maxLength={256}
              aria-describedby="new-password-hint"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            <p id="new-password-hint" className="text-sm text-muted-foreground">
              {t("setup.passwordHint")}
            </p>
          </div>
          <FormError message={message} />
          {done && (
            <p role="status" className="text-sm text-chart-5">
              {done}
            </p>
          )}
          <Button type="submit" className="self-start" disabled={change.isPending}>
            {first ? t("password.setSubmit") : t("password.submit")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function OIDCCard({
  me,
  name,
  origin,
  canSetPassword,
}: {
  me: Me;
  name: string;
  origin: string;
  canSetPassword: boolean;
}) {
  const { t } = useTranslation();
  const { oidc, oidc_error: oidcError } = route.useSearch();

  const link = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST("/api/v1/me/oidc");
      if (error) throw failure(error, response);
      return data;
    },
    // Навигация на провайдера: он вернёт браузер на /account с результатом.
    onSuccess: (data) => window.location.assign(data.authorize_url),
  });
  const unlink = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.DELETE("/api/v1/me/oidc");
      if (error) throw failure(error, response);
    },
    onSuccess: () => updateMe({ oidc_linked: false }),
  });

  let message: string | undefined;
  if (link.isError) message = errorText(t, link.error);
  else if (unlink.isError) message = errorText(t, unlink.error);
  else if (oidcError && !unlink.isSuccess) message = errorText(t, new ApiFailure(oidcError));
  let status: string | undefined;
  if (unlink.isSuccess) status = t("oidc.unlinkedDone");
  else if (oidc === "linked" && me.oidc_linked) status = t("oidc.linkedDone");

  // Cookie потока и сессия живут на своём хосте, а провайдер возвращает на публичный.
  const onPublicOrigin = window.location.origin === origin;
  // Без пароля отвязать нельзя; если и задать его негде (вход по паролю выключен), не предлагаем.
  const showUnlink = me.oidc_linked && (me.has_password || canSetPassword);

  return (
    <Card className="w-full max-w-md">
      <CardHeader>
        <CardTitle className="text-lg">{t("oidc.title", { name })}</CardTitle>
        <CardDescription>{me.oidc_linked ? t("oidc.linked", { name }) : t("oidc.notLinked", { name })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <FormError message={message} />
        {status && (
          <p role="status" className="text-sm text-chart-5">
            {status}
          </p>
        )}
        {!onPublicOrigin && (
          <p className="text-sm text-muted-foreground">
            {t("oidc.wrongHost")}{" "}
            <a className="text-primary underline-offset-4 hover:underline" href={`${origin}/account`}>
              {t("oidc.openPublic", { origin })}
            </a>
          </p>
        )}
        {(onPublicOrigin || showUnlink) && (
          <div className="flex flex-wrap gap-2">
            {onPublicOrigin && (
              <Button
                variant={me.oidc_linked ? "outline" : "default"}
                disabled={link.isPending || link.isSuccess}
                onClick={() => link.mutate()}
              >
                {me.oidc_linked ? t("oidc.relink") : t("oidc.link")}
              </Button>
            )}
            {/* Отвязка не ходит к провайдеру и работает с любого адреса. */}
            {showUnlink && (
              <Button
                variant="outline"
                disabled={!me.has_password || unlink.isPending}
                onClick={() => unlink.mutate()}
              >
                {t("oidc.unlink")}
              </Button>
            )}
          </div>
        )}
        {showUnlink && !me.has_password && (
          <p className="text-sm text-muted-foreground">{t("oidc.needPassword")}</p>
        )}
      </CardContent>
    </Card>
  );
}
