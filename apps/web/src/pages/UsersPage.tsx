import type { IssuedUserPassword, UserSummary } from "@ksb/api-client";
import { useQuery } from "@tanstack/react-query";
import {
  Ban,
  CircleCheck,
  EllipsisVertical,
  KeyRound,
  Link2Off,
  Plus,
  Shield,
  ShieldOff,
  Trash2,
  UserRound,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { meQuery, methodsQuery } from "@/auth";
import { ConfirmUserDialog, CreateUserDialog, UserPasswordDialog } from "@/components/UserDialogs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { formatDate, formatRelative } from "@/lib/format";
import { type UserAction, usersQuery } from "@/users";

type Action = { action: UserAction; user: UserSummary };

function UserMenu({
  user,
  canReset,
  onAction,
}: {
  user: UserSummary;
  canReset: boolean;
  onAction: (a: Action) => void;
}) {
  const { t } = useTranslation();
  const item = (action: UserAction, icon: ReactNode) => (
    <DropdownMenuItem
      variant={action === "delete" ? "destructive" : "default"}
      onSelect={() => onAction({ action, user })}
    >
      {icon}
      {t(`users.actions.${action}.item`)}
    </DropdownMenuItem>
  );
  return (
    // modal={false}: модальное меню снимает блокировку страницы одновременно с
    // открытием диалога, и pointer-events: none остаётся на body после его закрытия.
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="shrink-0"
          title={t("users.menu")}
          aria-label={`${t("users.menu")}: ${user.username}`}
        >
          <EllipsisVertical />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {user.role === "admin" ? item("demote", <ShieldOff />) : item("promote", <Shield />)}
        {user.disabled ? item("enable", <CircleCheck />) : item("disable", <Ban />)}
        {canReset && item("reset", <KeyRound />)}
        {user.oidc_linked && item("unlink", <Link2Off />)}
        <DropdownMenuSeparator />
        {item("delete", <Trash2 />)}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function UserRow({
  user,
  self,
  canReset,
  now,
  onAction,
}: {
  user: UserSummary;
  self: boolean;
  canReset: boolean;
  now: number;
  onAction: (a: Action) => void;
}) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const meta = [
    user.has_password ? t("users.hasPassword") : t("users.noPassword"),
    user.oidc_linked && t("users.oidc"),
    t("users.devices", { count: user.device_count }),
    t("users.created", { date: formatDate(user.created_at, lang) }),
    user.last_active_at
      ? t("users.lastActive", { when: formatRelative(user.last_active_at, lang, now) })
      : t("users.noActivity"),
  ].filter(Boolean);

  return (
    <li className="flex items-start gap-3 px-4 py-3.5">
      <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-secondary text-muted-foreground [&_svg]:size-4.5">
        <UserRound />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="max-w-full truncate font-medium">{user.username}</span>
          {self && <Badge variant="secondary">{t("users.you")}</Badge>}
          {user.role === "admin" && <Badge variant="outline">{t("roles.admin")}</Badge>}
          {user.disabled && <Badge variant="destructive">{t("users.disabled")}</Badge>}
        </div>
        <p className="text-xs text-muted-foreground">{meta.join(" · ")}</p>
      </div>
      {!self && <UserMenu user={user} canReset={canReset} onAction={onAction} />}
    </li>
  );
}

export function UsersPage() {
  const { t } = useTranslation();
  const { data: me } = useQuery(meQuery);
  const { data: methods } = useQuery(methodsQuery);
  const users = useQuery(usersQuery);
  const [creating, setCreating] = useState(false);
  const [action, setAction] = useState<Action | null>(null);
  const [issued, setIssued] = useState<IssuedUserPassword | null>(null);
  // Момент загрузки списка — точка отсчёта для «5 минут назад».
  const now = users.dataUpdatedAt;

  return (
    <>
      <div className="flex max-w-3xl flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{t("users.title")}</h1>
          <p className="text-sm text-pretty text-muted-foreground">{t("users.description")}</p>
        </div>
        <Button className="self-start" onClick={() => setCreating(true)}>
          <Plus />
          {t("users.add")}
        </Button>
      </div>

      {users.isPending && <p className="text-sm text-muted-foreground">{t("users.loading")}</p>}
      {users.isError && <p className="text-sm text-destructive">{t("users.loadError")}</p>}

      {users.data && (
        <div className="max-w-3xl">
          <Card className="gap-0 overflow-hidden py-0">
            <ul className="divide-y">
              {users.data.map((u) => (
                <UserRow
                  key={u.id}
                  user={u}
                  self={u.username === me?.username}
                  // Сервер ответил бы password_login_disabled: такой пароль не откроет панель.
                  canReset={!(methods?.password === "unlinked" && u.oidc_linked)}
                  now={now}
                  onAction={setAction}
                />
              ))}
            </ul>
          </Card>
        </div>
      )}

      {creating && (
        <CreateUserDialog
          onClose={() => setCreating(false)}
          onCreated={(r) => {
            setCreating(false);
            setIssued(r);
          }}
        />
      )}
      {action && (
        <ConfirmUserDialog
          action={action.action}
          user={action.user}
          onClose={() => setAction(null)}
          onIssued={(r) => {
            setAction(null);
            setIssued(r);
          }}
        />
      )}
      {issued && <UserPasswordDialog issued={issued} onClose={() => setIssued(null)} />}
    </>
  );
}
