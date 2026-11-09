import type { IssuedUserPassword, Role, UserSummary } from "@ksb/api-client";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { errorText } from "@/auth";
import { FormError } from "@/components/FormError";
import { Value } from "@/components/KoreaderSetup";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { type UserAction, useCreateUser, useUserAction } from "@/users";

export function CreateUserDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (issued: IssuedUserPassword) => void;
}) {
  const { t } = useTranslation();
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<Role>("user");
  const create = useCreateUser(onCreated);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent showCloseButton={false}>
        <form
          className="flex flex-col gap-5"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate({ username: username.trim(), role });
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("users.createDialog.title")}</DialogTitle>
            <DialogDescription>{t("users.createDialog.description")}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-name">{t("users.username")}</Label>
            <Input
              id="user-name"
              required
              autoFocus
              autoComplete="off"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-role">{t("users.role")}</Label>
            <Select value={role} onValueChange={(v) => setRole(v as Role)}>
              <SelectTrigger id="user-role" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t("roles.user")}</SelectItem>
                <SelectItem value="admin">{t("roles.admin")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <FormError message={create.isError ? errorText(t, create.error) : undefined} />
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                {t("users.cancel")}
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {t("users.createDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Подтверждение действия над пользователем; ошибка остаётся в диалоге. */
export function ConfirmUserDialog({
  action,
  user,
  onClose,
  onIssued,
}: {
  action: UserAction;
  user: UserSummary;
  onClose: () => void;
  onIssued: (issued: IssuedUserPassword) => void;
}) {
  const { t } = useTranslation();
  const confirm = useUserAction(action, user.id, (issued) => {
    if (issued) onIssued(issued);
    else onClose();
  });
  // Пояснения зависят от цели: у непривязанного и у пользователя с паролем их нет.
  let note: string | undefined;
  if ((action === "promote" || action === "demote") && user.oidc_linked) note = t("users.actions.roleOidcNote");
  if (action === "unlink" && !user.has_password) note = t("users.actions.unlink.noPasswordNote");
  if (action === "delete" && user.oidc_linked) note = t("users.actions.delete.oidcNote");

  return (
    <AlertDialog open onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(`users.actions.${action}.title`, { name: user.username })}</AlertDialogTitle>
          <AlertDialogDescription>{t(`users.actions.${action}.description`)}</AlertDialogDescription>
          {note && <p className="text-sm text-muted-foreground">{note}</p>}
        </AlertDialogHeader>
        <FormError message={confirm.isError ? errorText(t, confirm.error) : undefined} />
        <AlertDialogFooter>
          <AlertDialogCancel>{t("users.cancel")}</AlertDialogCancel>
          <Button
            variant={action === "delete" ? "destructive" : "default"}
            disabled={confirm.isPending}
            onClick={() => confirm.mutate()}
          >
            {t(`users.actions.${action}.confirm`)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Пароль показывается один раз, поэтому диалог закрывается только кнопкой. */
export function UserPasswordDialog({ issued, onClose }: { issued: IssuedUserPassword; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Dialog open>
      <DialogContent
        showCloseButton={false}
        className="max-h-[calc(100svh-2rem)] grid-cols-[minmax(0,1fr)] overflow-y-auto"
        onEscapeKeyDown={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>{t("users.passwordDialog.title", { name: issued.username })}</DialogTitle>
          <DialogDescription>{t("users.passwordDialog.once")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <Value label={t("users.passwordDialog.address")} value={window.location.origin} wrap />
          <Value label={t("users.username")} value={issued.username} />
          <Value label={t("users.passwordDialog.password")} value={issued.password} large wrap />
        </div>
        <p className="text-sm text-muted-foreground">{t("users.passwordDialog.devicesHint")}</p>
        <DialogFooter>
          <Button onClick={onClose}>{t("users.passwordDialog.done")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
