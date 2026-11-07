import type { Device, DeviceWithPassword } from "@ksb/api-client";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Ban, KeyRound, Pencil, Plus, TabletSmartphone } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { meQuery } from "@/auth";
import {
  ConfirmDeviceDialog,
  CreateDeviceDialog,
  devicesQuery,
  PasswordDialog,
  RenameDeviceDialog,
} from "@/components/DeviceDialogs";
import { StatisticsCard } from "@/components/StatisticsCard";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { formatDate, formatRelative } from "@/lib/format";

type Action = { kind: "rename" | "reset" | "revoke"; device: Device };

function DeviceRow({
  device,
  showOwner,
  now,
  onAction,
}: {
  device: Device;
  showOwner: boolean;
  now: number;
  onAction: (a: Action) => void;
}) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const revoked = device.revoked_at !== null;
  const meta = [
    showOwner && device.owner,
    t("devices.created", { date: formatDate(device.created_at, lang) }),
    device.last_used_at && t("devices.lastUsed", { when: formatRelative(device.last_used_at, lang, now) }),
    device.revoked_at && t("devices.revokedAt", { date: formatDate(device.revoked_at, lang) }),
  ].filter(Boolean);
  const actions = [
    { kind: "rename", icon: <Pencil />, label: t("devices.rename") },
    { kind: "reset", icon: <KeyRound />, label: t("devices.resetPassword") },
    { kind: "revoke", icon: <Ban />, label: t("devices.revoke") },
  ] as const;

  return (
    <li className="flex items-start gap-3 px-4 py-3.5">
      <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-secondary text-muted-foreground [&_svg]:size-4.5">
        <TabletSmartphone />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-medium">{device.name}</span>
          {revoked && <Badge variant="outline">{t("devices.revokedBadge")}</Badge>}
        </div>
        <p className="truncate text-sm text-muted-foreground" title={t("devices.reportedHint")}>
          {device.reported_device ?? t("devices.notSynced")}
        </p>
        <p className="text-xs text-muted-foreground">{meta.join(" · ")}</p>
      </div>
      {!revoked && (
        <div className="flex shrink-0 gap-0.5">
          {actions.map((a) => (
            <Button
              key={a.kind}
              variant="ghost"
              size="icon-sm"
              title={a.label}
              aria-label={`${a.label}: ${device.name}`}
              className={a.kind === "revoke" ? "hover:text-destructive" : undefined}
              onClick={() => onAction({ kind: a.kind, device })}
            >
              {a.icon}
            </Button>
          ))}
        </div>
      )}
    </li>
  );
}

function DeviceList({ devices, ...row }: { devices: Device[] } & Omit<Parameters<typeof DeviceRow>[0], "device">) {
  return (
    <Card className="gap-0 overflow-hidden py-0">
      <ul className="divide-y">
        {devices.map((d) => (
          <DeviceRow key={d.id} device={d} {...row} />
        ))}
      </ul>
    </Card>
  );
}

/** Группы по владельцу в порядке ответа сервера (он сортирует по имени). */
function byOwner(devices: Device[]): [string, Device[]][] {
  const groups = new Map<string, Device[]>();
  for (const d of devices) groups.set(d.owner, [...(groups.get(d.owner) ?? []), d]);
  return [...groups];
}

export function DevicesPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  const devices = useQuery(devicesQuery);
  const [creating, setCreating] = useState(false);
  const [action, setAction] = useState<Action | null>(null);
  const [issued, setIssued] = useState<DeviceWithPassword | null>(null);
  // Момент загрузки списка — точка отсчёта для «5 минут назад».
  const now = devices.dataUpdatedAt;

  const isAdmin = me?.role === "admin";
  const active = devices.data?.filter((d) => d.revoked_at === null) ?? [];
  const revoked = devices.data?.filter((d) => d.revoked_at !== null) ?? [];
  const row = { now, onAction: setAction };
  const addButton = (
    <Button className="self-start" onClick={() => setCreating(true)}>
      <Plus />
      {t("devices.add")}
    </Button>
  );

  return (
    <>
      <div className="flex max-w-3xl flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("devices.title")}</h1>
          <p className="text-sm text-pretty text-muted-foreground">{t("devices.description")}</p>
          <Link
            to="/password"
            className="flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground [&_svg]:size-4"
          >
            <KeyRound />
            {t("devices.changePassword")}
          </Link>
        </div>
        {active.length > 0 && addButton}
      </div>

      {devices.isPending && <p className="text-sm text-muted-foreground">{t("devices.loading")}</p>}
      {devices.isError && <p className="text-sm text-destructive">{t("devices.loadError")}</p>}

      {devices.isSuccess && active.length === 0 && (
        <Card className="max-w-3xl items-start gap-3 px-6">
          <h2 className="text-base font-medium">{t("devices.emptyTitle")}</h2>
          <p className="text-sm text-pretty text-muted-foreground">{t("devices.emptyText")}</p>
          <div className="pt-1">{addButton}</div>
        </Card>
      )}

      {active.length > 0 &&
        (isAdmin ? (
          byOwner(active).map(([owner, list]) => (
            <section key={owner} className="flex max-w-3xl flex-col gap-2.5">
              <h2 className="text-sm font-medium text-muted-foreground">{owner}</h2>
              <DeviceList devices={list} showOwner={false} {...row} />
            </section>
          ))
        ) : (
          <div className="max-w-3xl">
            <DeviceList devices={active} showOwner={false} {...row} />
          </div>
        ))}

      {revoked.length > 0 && (
        <details className="max-w-3xl">
          <summary className="cursor-pointer text-sm text-muted-foreground select-none hover:text-foreground">
            {t("devices.revoked", { count: revoked.length })}
          </summary>
          <div className="pt-3">
            <DeviceList devices={revoked} showOwner={isAdmin} {...row} />
          </div>
        </details>
      )}

      <StatisticsCard />

      {creating && me && (
        <CreateDeviceDialog
          me={me}
          onClose={() => setCreating(false)}
          onCreated={(r) => {
            setCreating(false);
            setIssued(r);
          }}
        />
      )}
      {action?.kind === "rename" && <RenameDeviceDialog device={action.device} onClose={() => setAction(null)} />}
      {(action?.kind === "reset" || action?.kind === "revoke") && (
        <ConfirmDeviceDialog
          kind={action.kind}
          device={action.device}
          onClose={() => setAction(null)}
          onIssued={(r) => {
            setAction(null);
            setIssued(r);
          }}
        />
      )}
      {issued && <PasswordDialog issued={issued} onClose={() => setIssued(null)} />}
    </>
  );
}
