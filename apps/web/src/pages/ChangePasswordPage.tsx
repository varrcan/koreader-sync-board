import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, ApiFailure, failure } from "@/api";
import { errorText } from "@/auth";
import { FormError } from "@/components/FormError";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function ChangePasswordPage() {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");

  const change = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.PUT("/api/v1/me/password", {
        body: { current_password: current, new_password: next },
      });
      if (error) throw failure(error, response);
    },
    onSuccess: () => {
      setCurrent("");
      setNext("");
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
        <CardTitle className="text-lg">{t("password.title")}</CardTitle>
        <CardDescription>{t("password.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            change.mutate();
          }}
        >
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
          {change.isSuccess && (
            <p role="status" className="text-sm text-chart-5">
              {t("password.changed")}
            </p>
          )}
          <Button type="submit" className="self-start" disabled={change.isPending}>
            {t("password.submit")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
