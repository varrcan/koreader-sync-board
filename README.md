# koreader-sync-board

A lightweight self-hosted server for the **standard** KOReader — no plugins on the device.

- **Progress sync** compatible with the KOReader sync protocol (the same API as
  `koreader/koreader-sync-server`): point KOReader's built-in *Progress sync* at this server.
- **Every update is kept**: progress history is append-only, and progress for books the
  server has never seen is stored too.
- **One account, many devices**: each e-reader gets its own password that can be revoked
  on its own.
- One static binary (linux/amd64, linux/arm64), SQLite, no Node at runtime.

Reading statistics, the web panel and the library are in development: for now the panel
has sign-in, the first-run setup and password change.

## Run with Docker

```sh
docker build -t koreader-sync-board .
docker run -d --name ksb \
  -v ksb-data:/data \
  -p 8080:8080 \
  koreader-sync-board
```

Use a **named volume** (`ksb-data` above): Docker gives it the right owner on first use.
The container runs as the unprivileged user `65532`. With a bind mount, make the host
directory writable for that user first:

```sh
mkdir -p ./data && sudo chown 65532:65532 ./data
docker run -d --name ksb -v "$PWD/data:/data" -p 8080:8080 koreader-sync-board
```

The image has a built-in health check (`ksb healthcheck`, which requests `/api/v1/health`).

## First run and signing in to the panel

Open `http://<host>:8080`. On a fresh server with no users the panel shows the **setup
wizard**: it asks for a one-time setup code that the server writes to its log on startup.
Find it with

```sh
docker logs ksb 2>&1 | grep setup_code          # Docker
journalctl -u ksb | grep setup_code             # binary under systemd (ksb.service)
```

Enter the code (case, spaces and dashes do not matter), choose a username and a panel
password of 8 to 256 characters: this creates the administrator and signs you in. The code
lives only in memory, so after a restart look for the newest line. To skip the wizard, set
`ADMIN_USER` and `ADMIN_PASSWORD` (both, or neither) before the first start.

The **panel password** is separate from device passwords: a device password does not open
the panel, and the panel password does not work in KOReader. Users created with
`ksb user add` (and users from earlier versions) have no panel password yet; issue one with

```sh
docker exec ksb /ksb user passwd alice
```

It prints a generated password `xxxx-xxxx-xxxx-xxxx` **once** and signs the user out of
all panel sessions; device passwords stay as they are. After signing in, the password can be
changed in the panel, which signs out the user's other sessions.

A panel session lasts 30 days from the last use. The session cookie gets the `Secure` flag
when the panel is opened over HTTPS (directly or through a proxy that sends
`X-Forwarded-Proto: https`); over plain `http://` in a local network it works without it.

## Signing in with OIDC (Zitadel, Keycloak, Authentik …)

The panel can sign users in through one OpenID Connect provider (Authorization Code flow
with PKCE). KOReader is not affected: e-readers keep using device passwords. OIDC is enabled
by three variables:

```sh
docker run ... \
  -e KSB_OIDC_ISSUER=https://id.example.com \
  -e KSB_OIDC_CLIENT_ID=123456789@books \
  -e KSB_PUBLIC_URL=https://books.example.com \
  -e KSB_OIDC_NAME=Zitadel \
  koreader-sync-board
```

Register this **redirect URI** with the provider:

```
https://books.example.com/api/v1/auth/oidc/callback
```

`KSB_PUBLIC_URL` is the address users open the panel at: an origin only (`https://host` or
`http://host:port`), without a path — serving the panel under a path prefix is not
supported. If someone opens the panel by another address (a LAN IP, `localhost`), the sign-in
button first sends them to `KSB_PUBLIC_URL` and the whole sign-in happens there; linking an
account (see below) only works at the public address, and the panel shows a link to it.

The server does not contact the provider on startup: discovery happens on the first sign-in,
and if the provider is down the sign-in fails with a message and is retried next time, while
the server and KOReader sync keep working.

**Who gets in.** A panel user is tied to the provider account by its issuer and `sub`, never
by name or email. There are two ways to tie them:

- an existing user signs in with the panel password, opens **Account** and clicks **Link**;
  **Unlink** removes the link (allowed only when the user has a panel password);
- with `KSB_OIDC_AUTO_CREATE=true`, the first sign-in of an unknown provider account creates a
  panel user named after the `preferred_username` claim (`KSB_OIDC_USERNAME_CLAIM`), with no
  panel password and no devices. If that name is already taken, the sign-in is rejected and the
  existing user stays untouched — sign in as that user with the password and link instead. A
  user created this way can set a panel password under **Account**.

The first administrator is still created by the setup wizard or `ADMIN_USER`/`ADMIN_PASSWORD`;
before that, OIDC sign-in is refused. Who may sign in at all is decided by the provider (for
example, which users are granted the project in Zitadel).

**Admin role from a claim.** With `KSB_OIDC_ADMIN_CLAIM` and `KSB_OIDC_ADMIN_VALUE` set, every
OIDC sign-in (and linking) makes the user `admin` if the claim in the ID token matches and
`user` otherwise; the change is logged. A match is a string equal to the value, an array
containing it, or an object with it as a key. Without these variables OIDC never changes roles.
If the provider dropped the role by mistake, restore it with
`ksb user role alice admin` (the next OIDC sign-in applies the claim again).

**Turning off password sign-in.** `KSB_PANEL_PASSWORD_LOGIN=false` (only together with OIDC)
disables the panel password for users **linked** to the provider; users without a link still
sign in with their password, so you cannot lock yourself out. A safe switch:

1. create the administrator with the setup wizard or `ADMIN_USER`/`ADMIN_PASSWORD`;
2. sign in with the password and link the provider account under **Account**;
3. restart with `KSB_PANEL_PASSWORD_LOGIN=false`.

Signing out of the panel ends only the panel session, not the session at the provider;
disabling a user at the provider does not end their open panel sessions — use
`ksb user disable` for that.

**Changing the issuer.** Links are tied to the issuer URL. After `KSB_OIDC_ISSUER` changes (a
new domain for the provider), old links no longer match: remove them with
`ksb user oidc-unlink alice` and link again.

### Zitadel

1. In a project, create an application of type **Web**: authentication method **PKCE**
   (no secret) or **Code** (then set `KSB_OIDC_CLIENT_SECRET`). Add the redirect URI above.
2. In the application's token settings enable **User Info inside ID Token** (the username
   comes from the ID token) and, for the role claim, **User roles inside ID Token**.
3. For the role claim, enable **Assert Roles on Authentication** in the project, create a
   role (for example `admin`) and grant it to the administrators. Then set

   ```sh
   KSB_OIDC_ADMIN_CLAIM=urn:zitadel:iam:org:project:roles
   KSB_OIDC_ADMIN_VALUE=admin
   ```

`KSB_OIDC_ISSUER` is the instance URL, for example `https://id.example.com`; the client ID is
shown on the application page. For Keycloak or Authentik a `groups` claim works the same way:
`KSB_OIDC_ADMIN_CLAIM=groups`, `KSB_OIDC_ADMIN_VALUE=ksb-admins`.

## Create a user and a device

Users and devices are managed with the `ksb` CLI. It works on the same database while the
server is running, and changes (for example, a revoked device) apply on the next request.

```sh
docker exec ksb /ksb user add alice
docker exec ksb /ksb device add alice "Kindle"
```

`device add` prints the device password **once**, in the form `xxxx-xxxx` (8 random
characters, easy to type on an e-reader keyboard); it is not stored in readable form and
cannot be shown again. Passwords issued by earlier versions in a longer format keep working. Create a separate device for each e-reader:

```sh
docker exec ksb /ksb device list alice           # names, creation and last use, revoked flag
docker exec ksb /ksb device revoke alice "Kindle"
docker exec ksb /ksb user disable alice          # blocks all of alice's devices and panel sessions
docker exec ksb /ksb user delete alice           # removes devices, reading progress and statistics
docker exec ksb /ksb user role alice admin       # or user
docker exec ksb /ksb user oidc-unlink alice      # removes the OIDC link; `user list` shows links
```

## Configure KOReader

1. Open a book, then **Tools → Progress sync → Custom sync server** and enter
   `http://<host>:8080`.
2. **Register / Login** → enter the username and the **device password** from
   `ksb device add`, then choose **Login**. (Registration from the device is disabled by
   default; see `KSB_KOSYNC_REGISTRATION`.)
3. Optional, KOReader v2026.07 and later: enable **Send document metadata** so the
   server receives the filename, title and authors with each update. Without it the
   server only knows the document hash.

Both document matching methods (*Binary* and *Filename*) are supported.

### Reading statistics

KOReader sends its reading statistics (`statistics.sqlite3`: time and pages per book) through
its built-in cloud sync over WebDAV. The server accepts it at `/dav` with the same device
password; no plugin is needed.

1. **Cloud storage** → add a **WebDAV** server: address `http://<host>:8080/dav`, the username
   and the **device password**. Leave the start folder empty. KOReader 2026.07 has two such
   menu items: use **Cloud storage+**. The older **Cloud storage** app keeps its own list of
   servers, and statistics sync does not see it.
2. **Reading statistics → Settings → Cloud sync** → pick this server, then in the folder list
   long-press **Long-press here to choose current folder**.
3. **Reading statistics → Sync now**. Repeat whenever you want to sync; KOReader has no timer.

Several e-readers of one user merge their statistics through the server: KOReader downloads
the file, merges it with its own and uploads the result. The vocabulary builder
(`vocabulary_builder.sqlite3`) syncs the same way and is stored as is. Files are limited to
64 MiB.

- Use a password issued by the panel or `ksb device add`. KOReader URL-decodes the WebDAV
  password, so a password containing `%` (possible for one set from KOReader with
  **Register** or a password change) works for progress sync but not for WebDAV.
- The panel shows when statistics were last synced and can upload a `statistics.sqlite3`
  by hand (for example, from an old e-reader). The upload is merged into the statistics on
  the server without deleting anything, and readers get it on their next sync.
- Books synced without **Send document metadata** get their title and authors from the
  statistics.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `KSB_LISTEN` | `:8080` | Listen address |
| `KSB_DATA_DIR` | `/data` | Data directory (SQLite database `ksb.sqlite3`) |
| `KSB_KOSYNC_REGISTRATION` | `false` | Allow creating accounts from KOReader (`POST /users/create`) |
| `KSB_KOSYNC_SELF_DELETE` | `false` | Allow deleting an account from the device (`DELETE /users/me`) |
| `KSB_TRUSTED_PROXIES` | — | Reverse proxies whose `X-Forwarded-For` is trusted: CIDRs or single addresses, comma-separated (see below) |
| `ADMIN_USER`, `ADMIN_PASSWORD` | — | Create an admin with a panel password on first start (empty database only). Set both or neither; the password needs at least 8 characters |
| `KSB_OIDC_ISSUER` | — | OIDC provider URL; enables OIDC together with `KSB_OIDC_CLIENT_ID` and `KSB_PUBLIC_URL` |
| `KSB_OIDC_CLIENT_ID` | — | Client ID at the provider |
| `KSB_OIDC_CLIENT_SECRET` | — | Client secret; empty for a public client with PKCE only |
| `KSB_PUBLIC_URL` | — | Public origin of the panel without a path, for the redirect URI |
| `KSB_OIDC_NAME` | `SSO` | Label of the sign-in button: "Sign in with …" |
| `KSB_OIDC_SCOPES` | `openid profile email` | Space-separated; `openid` is always added |
| `KSB_OIDC_USERNAME_CLAIM` | `preferred_username` | Claim for the name of an auto-created user |
| `KSB_OIDC_AUTO_CREATE` | `false` | Create a panel user on the first sign-in of an unknown provider account |
| `KSB_OIDC_ADMIN_CLAIM`, `KSB_OIDC_ADMIN_VALUE` | — | Set the admin role from this ID token claim on every OIDC sign-in. Set both or neither |
| `KSB_PANEL_PASSWORD_LOGIN` | `true` | `false` disables the panel password for users linked to OIDC (requires OIDC) |

`serve` also accepts `-listen` and `-data-dir`, which override the environment.

## Failed logins

After **10 failed logins within 10 minutes** from one client address, that address is
blocked for **15 minutes**. KOReader logins (progress sync and WebDAV), panel sign-ins (a wrong password, including the
current password when changing it) and wrong setup codes share this budget. IPv6 clients are counted per `/64` network. Counters live in
memory and reset on restart.

- KOReader shows the reason: *"Too many failed attempts. Try again in N minutes."*
  (status 401, code 2001, plus a `Retry-After` header for other clients). A blocked
  `POST /users/create` gets 402/2005 with a similar text.
- A device that has logged in recently keeps syncing from a blocked address: the server
  remembers successful checks, and every request extends that.
- Sending the **same** wrong password again (for example, a forgotten e-reader whose device
  was revoked) is answered at once and does not count towards the limit, so it cannot
  lock out the other e-readers in the same home network.
- With registration enabled, every `POST /users/create` counts as an attempt, successful
  ones too.
- The panel answers a blocked address with `429 Too Many Requests` and `Retry-After`.
  Unlike KOReader, a repeated wrong panel password counts every time.
- Failed OIDC sign-ins do not count and are logged as `panel: oidc login failed` with a
  `reason`, not as `authorization failed`: a refusal at the provider is not password guessing.

## Behind a reverse proxy

Behind nginx, nginx proxy manager, Caddy or Traefik every request comes from the proxy's
address. Tell the server which addresses are your proxy, so that it takes the client
address from `X-Forwarded-For`:

```sh
docker run ... -e KSB_TRUSTED_PROXIES=172.17.0.0/16 koreader-sync-board
```

List **only the proxy itself** (its address, or the Docker network it reaches the server
from). The header is read right to left, and the first address that is not a trusted proxy
is the client; for a request from a non-trusted address the header is ignored. The proxy
must append the client address, as nginx does with
`proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` (nginx proxy manager does
this by default).

The proxy must not strip or rewrite the `ETag` response header and the `If-Match` request
header on `/dav/`, and must allow request bodies up to 64 MiB (nginx:
`client_max_body_size 64m;`). Without `ETag` KOReader uploads without `If-Match`, gets `412`
and retries forever.

The proxy must also pass the original `Host` header (nginx: `proxy_set_header Host $host;`;
nginx proxy manager, Caddy and Traefik do this by default). The panel rejects sign-in and
other changes from a foreign site; over plain `http://` browsers do not send
`Sec-Fetch-Site`, so the server compares `Origin` with `Host`, and a rewritten `Host` makes
every sign-in fail with `cross_origin`.

Getting `KSB_TRUSTED_PROXIES` wrong has consequences either way:

- **not set** behind a proxy: all clients share the proxy's address, so one attacker blocks
  logins for everyone whose device has not logged in recently, and the log shows only the
  proxy;
- **too broad** (for example `0.0.0.0/0`): any client can put an arbitrary address into
  `X-Forwarded-For` and bypass the limit.

### fail2ban

Every failed login is logged as one JSON line with the client address:

```json
{"time":"2026-11-04T21:47:10.84+03:00","level":"WARN","msg":"kosync: authorization failed","user":"alice","ip":"198.51.100.7","path":"/users/auth","reason":"unauthorized"}
{"time":"2026-11-04T21:48:02.11+03:00","level":"WARN","msg":"panel: authorization failed","user":"alice","ip":"198.51.100.7","path":"/api/v1/auth/login","reason":"unauthorized"}
{"time":"2026-11-04T21:49:31.52+03:00","level":"WARN","msg":"webdav: authorization failed","user":"alice","ip":"198.51.100.7","path":"/dav/statistics.sqlite3","reason":"unauthorized"}
```

Repeats of an already rejected password and requests refused because of a block are not
logged. To ban persistent offenders for longer, create `/etc/fail2ban/filter.d/ksb.conf`:

```ini
[Definition]
# Matches the plain JSON line and Docker's json-file log, where quotes are escaped.
failregex = \\?"msg\\?":\\?"(?:kosync|panel|webdav): authorization failed\\?".*\\?"ip\\?":\\?"<HOST>\\?",\\?"path\\?"
datepattern = "time\\?":\\?"%%Y-%%m-%%dT%%H:%%M:%%S(?:\.\d+)?%%z
```

and `/etc/fail2ban/jail.d/ksb.local`:

```ini
[ksb]
enabled  = true
filter   = ksb
# Docker's default json-file driver; for a binary under systemd use
# backend = systemd and journalmatch = _SYSTEMD_UNIT=ksb.service instead.
logpath  = /var/lib/docker/containers/*/*-json.log
maxretry = 30
findtime = 1h
bantime  = 1d
port     = http,https
# Ports published by Docker bypass the INPUT chain.
chain    = DOCKER-USER
```

Ban on the host that receives the client connections: with a reverse proxy, that is the
proxy's host. Check the filter against your log with
`fail2ban-regex /var/lib/docker/containers/<id>/<id>-json.log /etc/fail2ban/filter.d/ksb.conf`.

## Talking to the sync API with curl

Like the reference server, the sync API requires a versioned `Accept` header and
authenticates with the **MD5 of the device password**:

```sh
KEY=$(printf %s 'abcd-efgh' | md5sum | cut -d' ' -f1)
curl -H 'Accept: application/vnd.koreader.v1+json' \
     -H 'x-auth-user: alice' -H "x-auth-key: $KEY" \
     http://localhost:8080/users/auth
# {"authorized":"OK"}
```

Without the header the server answers `412 {"code":100,"message":"Accept header not set."}`.

## Development

Requirements: Go 1.27, Node 22+ with pnpm, [Task](https://taskfile.dev). LuaJIT is only
needed to regenerate the partial MD5 reference data.

```sh
pnpm install
task build                 # web panel + bin/ksb with the panel embedded
task build:placeholder     # bin/ksb without Node, placeholder page
task test                  # Go tests (task test:full adds the >1 GiB sparse-file case)
task lint                  # golangci-lint + eslint
task typecheck             # tsc --noEmit for all TS packages
task generate              # regenerate Go and TS code from api/openapi.yaml
pnpm -F web dev            # dev server for the panel, proxies /api to :8080
```

Layout: `server/` (Go module), `apps/web` (Vite + React), `packages/api-client`
(generated from `api/openapi.yaml`), `packages/i18n` (ru/en).

Before touching the sync endpoints, read [docs/koreader-compat.md](docs/koreader-compat.md):
error order and codes, document hashes, and quirks of KOReader and CrossPoint clients.

## License

[AGPL-3.0](LICENSE)
