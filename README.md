# koreader-sync-board

A lightweight self-hosted server for the **standard** KOReader — no plugins on the device.

- **Progress sync** compatible with the KOReader sync protocol (the same API as
  `koreader/koreader-sync-server`): point KOReader's built-in *Progress sync* at this server.
- **Every update is kept**: progress history is append-only, and progress for books the
  server has never seen is stored too.
- **One account, many devices**: each e-reader gets its own password that can be revoked
  on its own.
- One static binary (linux/amd64, linux/arm64), SQLite, no Node at runtime.

Reading statistics, the web panel and the library are in development: the current build
ships a placeholder page.

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

## Create a user and a device

Users and devices are managed with the `ksb` CLI. It works on the same database while the
server is running, and changes (for example, a revoked device) apply on the next request.

```sh
docker exec ksb /ksb user add alice
docker exec ksb /ksb device add alice "Kindle"
```

`device add` prints the device password **once**; it is not stored in readable form and
cannot be shown again. Create a separate device for each e-reader:

```sh
docker exec ksb /ksb device list alice           # names, creation and last use, revoked flag
docker exec ksb /ksb device revoke alice "Kindle"
docker exec ksb /ksb user disable alice          # blocks all of alice's devices
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
| `ADMIN_USER`, `ADMIN_PASSWORD` | — | Create an admin with a panel password on first start (empty database only) |

`serve` also accepts `-listen` and `-data-dir`, which override the environment.

## Talking to the sync API with curl

Like the reference server, the sync API requires a versioned `Accept` header and
authenticates with the **MD5 of the device password**:

```sh
KEY=$(printf %s 'abcd-efgh-ijkl-mnop' | md5sum | cut -d' ' -f1)
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

## License

[AGPL-3.0](LICENSE)
