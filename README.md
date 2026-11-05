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
docker exec ksb /ksb user delete alice           # removes devices and reading progress
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

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `KSB_LISTEN` | `:8080` | Listen address |
| `KSB_DATA_DIR` | `/data` | Data directory (SQLite database `ksb.sqlite3`) |
| `KSB_KOSYNC_REGISTRATION` | `false` | Allow creating accounts from KOReader (`POST /users/create`) |
| `KSB_KOSYNC_SELF_DELETE` | `false` | Allow deleting an account from the device (`DELETE /users/me`) |
| `KSB_TRUSTED_PROXIES` | — | Reverse proxies whose `X-Forwarded-For` is trusted: CIDRs or single addresses, comma-separated (see below) |
| `ADMIN_USER`, `ADMIN_PASSWORD` | — | Create an admin with a panel password on first start (empty database only). Set both or neither; the password needs at least 8 characters |

`serve` also accepts `-listen` and `-data-dir`, which override the environment.

## Failed logins

After **10 failed logins within 10 minutes** from one client address, that address is
blocked for **15 minutes**. KOReader logins, panel sign-ins (a wrong password, including the
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
```

Repeats of an already rejected password and requests refused because of a block are not
logged. To ban persistent offenders for longer, create `/etc/fail2ban/filter.d/ksb.conf`:

```ini
[Definition]
# Matches the plain JSON line and Docker's json-file log, where quotes are escaped.
failregex = \\?"msg\\?":\\?"(?:kosync|panel): authorization failed\\?".*\\?"ip\\?":\\?"<HOST>\\?",\\?"path\\?"
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
