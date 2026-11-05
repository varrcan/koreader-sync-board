import type { Device, DeviceWithPassword, Me, UserSummary } from "@ksb/api-client";
import { queryOptions, useMutation, useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, failure } from "@/api";
import { errorText, queryClient } from "@/auth";
import { FormError } from "@/components/FormError";
import { MenuLabel, Value } from "@/components/KoreaderSetup";
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

export const devicesQuery = queryOptions({
  queryKey: ["devices"],
  queryFn: async (): Promise<Device[]> => {
    const { data, error, response } = await api.GET("/api/v1/devices");
    if (error) throw failure(error, response);
    return data;
  },
});

const usersQuery = queryOptions({
  queryKey: ["users"],
  queryFn: async (): Promise<UserSummary[]> => {
    const { data, error, response } = await api.GET("/api/v1/users");
    if (error) throw failure(error, response);
    return data;
  },
});

function invalidateDevices() {
  return queryClient.invalidateQueries({ queryKey: devicesQuery.queryKey });
}

function NameField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor="device-name">{t("devices.name")}</Label>
      <Input
        id="device-name"
        required
        autoFocus
        maxLength={64}
        autoComplete="off"
        placeholder={t("devices.namePlaceholder")}
        aria-describedby="device-name-hint"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <p id="device-name-hint" className="text-sm text-muted-foreground">
        {t("devices.nameHint")}
      </p>
    </div>
  );
}

/** Создание; администратор выбирает владельца среди включённых пользователей. */
export function CreateDeviceDialog({
  me,
  onClose,
  onCreated,
}: {
  me: Me;
  onClose: () => void;
  onCreated: (issued: DeviceWithPassword) => void;
}) {
  const { t } = useTranslation();
  const isAdmin = me.role === "admin";
  const [name, setName] = useState("");
  const [owner, setOwner] = useState(me.username);
  const users = useQuery({ ...usersQuery, enabled: isAdmin });
  const owners = users.data?.filter((u) => !u.disabled).map((u) => u.username) ?? [me.username];

  const create = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST("/api/v1/devices", {
        body: { name, username: owner === me.username ? undefined : owner },
      });
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (issued) => {
      void invalidateDevices();
      onCreated(issued);
    },
  });

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent showCloseButton={false}>
        <form
          className="flex flex-col gap-5"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("devices.createDialog.title")}</DialogTitle>
            <DialogDescription>{t("devices.createDialog.description")}</DialogDescription>
          </DialogHeader>
          <NameField value={name} onChange={setName} />
          {isAdmin && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="device-owner">{t("devices.owner")}</Label>
              <Select value={owner} onValueChange={setOwner}>
                <SelectTrigger id="device-owner" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {owners.map((u) => (
                    <SelectItem key={u} value={u}>
                      {u}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <FormError message={create.isError ? errorText(t, create.error) : undefined} />
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                {t("devices.cancel")}
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {t("devices.createDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function RenameDeviceDialog({ device, onClose }: { device: Device; onClose: () => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState(device.name);
  const rename = useMutation({
    mutationFn: async () => {
      const { error, response } = await api.PATCH("/api/v1/devices/{id}", {
        params: { path: { id: device.id } },
        body: { name },
      });
      if (error) throw failure(error, response);
    },
    onSuccess: () => {
      void invalidateDevices();
      onClose();
    },
  });

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent showCloseButton={false}>
        <form
          className="flex flex-col gap-5"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("devices.renameDialog.title")}</DialogTitle>
          </DialogHeader>
          <NameField value={name} onChange={setName} />
          <FormError message={rename.isError ? errorText(t, rename.error) : undefined} />
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                {t("devices.cancel")}
              </Button>
            </DialogClose>
            <Button type="submit" disabled={rename.isPending}>
              {t("devices.renameDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Подтверждение отзыва или выдачи нового пароля; ошибка остаётся в диалоге. */
export function ConfirmDeviceDialog({
  kind,
  device,
  onClose,
  onIssued,
}: {
  kind: "revoke" | "reset";
  device: Device;
  onClose: () => void;
  onIssued: (issued: DeviceWithPassword) => void;
}) {
  const { t } = useTranslation();
  const path = { params: { path: { id: device.id } } };
  const confirm = useMutation({
    mutationFn: async () => {
      if (kind === "revoke") {
        const { error, response } = await api.DELETE("/api/v1/devices/{id}", path);
        if (error) throw failure(error, response);
        return undefined;
      }
      const { data, error, response } = await api.POST("/api/v1/devices/{id}/password", path);
      if (error) throw failure(error, response);
      return data;
    },
    onSuccess: (issued) => {
      void invalidateDevices();
      if (issued) onIssued(issued);
      else onClose();
    },
  });
  const texts = kind === "revoke" ? "revokeDialog" : "resetDialog";

  return (
    <AlertDialog open onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(`devices.${texts}.title`, { name: device.name })}</AlertDialogTitle>
          <AlertDialogDescription>{t(`devices.${texts}.description`)}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormError message={confirm.isError ? errorText(t, confirm.error) : undefined} />
        <AlertDialogFooter>
          <AlertDialogCancel>{t("devices.cancel")}</AlertDialogCancel>
          <Button
            variant={kind === "revoke" ? "destructive" : "default"}
            disabled={confirm.isPending}
            onClick={() => confirm.mutate()}
          >
            {t(`devices.${texts}.confirm`)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-secondary text-xs font-semibold">
        {n}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-3 pt-0.5 text-sm text-muted-foreground">{children}</div>
    </li>
  );
}

/**
 * Пароль показывается один раз, поэтому карточка закрывается только кнопкой:
 * клик мимо или Esc не должны терять его до того, как человек его сохранил.
 */
export function PasswordDialog({ issued, onClose }: { issued: DeviceWithPassword; onClose: () => void }) {
  const { t } = useTranslation();
  const { device, password } = issued;
  return (
    <Dialog open>
      <DialogContent
        showCloseButton={false}
        className="max-h-[calc(100svh-2rem)] grid-cols-[minmax(0,1fr)] overflow-y-auto sm:max-w-lg"
        onEscapeKeyDown={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>{t("devices.passwordCard.title", { name: device.name })}</DialogTitle>
          <DialogDescription>{t("devices.passwordCard.once")}</DialogDescription>
        </DialogHeader>
        <section className="flex flex-col gap-4">
          <h2 className="text-xs font-medium tracking-wider text-muted-foreground uppercase">
            {t("devices.koreader.title")}
          </h2>
          <ol className="flex flex-col gap-4">
            <Step n={1}>
              <p>
                {t("devices.koreader.step1")} <MenuLabel item="progressSync" />
              </p>
            </Step>
            <Step n={2}>
              <p>
                <MenuLabel item="customServer" /> {t("devices.koreader.step2")}
              </p>
              <Value label={t("devices.koreader.server")} value={window.location.origin} />
              <p className="text-xs">{t("devices.koreader.serverHint")}</p>
            </Step>
            <Step n={3}>
              <p>
                <MenuLabel item="registerLogin" /> {t("devices.koreader.step3")} <MenuLabel item="login" />
              </p>
              <Value label={t("devices.koreader.username")} value={device.owner} />
              <Value label={t("devices.koreader.password")} value={password} large />
            </Step>
            <Step n={4}>
              <p>
                {t("devices.koreader.step4")} <MenuLabel item="sendMetadata" /> {t("devices.koreader.step4Hint")}
              </p>
            </Step>
          </ol>
        </section>
        <DialogFooter>
          <Button onClick={onClose}>{t("devices.passwordCard.done")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
