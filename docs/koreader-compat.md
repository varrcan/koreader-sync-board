# KOReader compatibility notes

What the server has to get right to work with an unmodified KOReader (and other
clients that speak the same protocol). Read this before changing anything under
`/users/*`, `/syncs/*` or `/healthcheck`.

Reference implementation: [koreader/koreader-sync-server](https://github.com/koreader/koreader-sync-server)
(`config/routes.lua`, `config/errors.lua`, `app/controllers/1/syncs_controller.lua`).
Client: `plugins/kosync.koplugin` in [koreader/koreader](https://github.com/koreader/koreader)
(`api.json`, `KOSyncClient.lua`, `main.lua`).

## The sync protocol (kosync)

### Wire format is fixed

Responses match the reference server byte for byte where the client can see the
difference. Unknown fields sent by third-party clients are ignored; nothing is
added to responses.

Endpoints: `POST /users/create`, `GET /users/auth`, `PUT /syncs/progress`,
`GET /syncs/progress/:document`, `PUT /users/password`, `DELETE /users/me`,
`GET /healthcheck`. KOReader itself only uses the first four.

### Order of checks

The reference server runs on a framework (gin) whose middleware decides the order
of errors. A request is processed as:

1. **Body** — invalid JSON → `400 {"code":103}`, a non-empty JSON array → `104`.
   An empty body, `null`, a scalar or `[]` count as an empty object.
2. **`Accept`** — missing → `412/100`, wrong shape → `412/101`, version other than 1
   → `412/102`. The `+` in `application/vnd.koreader.v1+json` is literal.
3. **Route** — unknown path or wrong method → `404`.
4. **Controller** — authentication and field checks.

So a broken body without `Accept` is a `400`, not a `412`.

### Error codes

| Code | HTTP | Meaning |
|------|------|---------|
| 2001 | 401 | Unauthorized |
| 2002 | 402 | Username already registered |
| 2003 | 403 | Invalid request (missing fields, bad username) |
| 2004 | 403 | Field `document` missing or contains `:` |
| 2005 | 402 | Registration disabled |
| 2006 | 404 | Account not found |
| 2007 | 403 | Document contains characters outside `[A-Za-z0-9_]` |

The client only accepts the statuses listed in `api.json` (`expected_status`:
`[200, 401]` for auth, `[200, 202, 401]` for push, `[201, 402]` for register).
Any other status becomes an exception and the user sees "Unknown server error".
That is why a rate-limited address gets `401/2001` (`402/2005` on registration)
with its own `message` and a `Retry-After` header, rather than `429`. KOReader
shows `message` on login.

### Authentication

Headers `x-auth-user` and `x-auth-key`. The key is **MD5 of the password**, not
the password itself. A user may have several devices, each with its own
password; the key matches any non-revoked device of that user. Device keys are
stored as argon2id of the MD5.

### Progress

`PUT /syncs/progress` takes `document`, `progress` (an xpointer for reflowable
formats, a page number otherwise), `percentage`, `device`, optional `device_id`
and, since KOReader 2026.07 with "Send document metadata" enabled, an optional
`metadata` object `{filename, title, authors}`. `percentage` may arrive as a
string and is stored as a number.

The client sends no timestamp. The server stamps the time of receipt and returns
it. Like the reference server, a `GET` without a recorded `device_id` keeps the
last one that was sent.

### Offline queue

With no network, KOReader 2026.07+ puts the push into `KOSyncQueue` (one entry per
book per day, kept up to four weeks) and drains it on reconnect. The server sees
several days of reading as a burst of requests within seconds, all stamped with
the time of receipt. The newest one wins, as in the reference server.

A push that fails with `401` is not queued, so an auth failure loses that
position; the next successful push brings the current one. Released versions up
to 2026.07.2 can drop a queued item when its retry fails.

### When KOReader pushes

With auto sync on (off by default), progress is pushed on closing a book,
suspend and network disconnect, and optionally every N pages; automatic pushes
are at least 25 s apart. A long reading session therefore produces dozens of
events for one book on one device. The activity feed folds a run of pushes with
gaps under 30 minutes into one entry for that reason.

## Document hashes

`document` is a hash, not a book title. Two hashing modes exist.

**Partial MD5** (KOReader's default, `util.partialMD5` in `frontend/util.lua`):
MD5 over up to 1 KiB read at offsets `1024 << 2i` for `i = -1 … 10`.

- At `i = -1` LuaJIT's shift gives offset **0**, not 256.
- A sample that starts inside the file but runs past the end is hashed truncated.
- The loop stops at the first read that returns nothing.
- KOReader caches the hash in the book's `.sdr` folder on first open.

The hash is tied to the file byte for byte: an FB2 and an EPUB of the same book
are different documents, and a position cannot be carried between them (an
xpointer is format-specific).

**Filename MD5**: `md5(basename)` with the extension. KOReader uses it when set
to; CrossPoint uses it by default. File systems disagree on Unicode
normalization, so match both NFC and NFD forms of the name.

Progress for a hash the server knows nothing about is always stored and shown as
an "unknown book". The server never rejects a document for being unknown.

## Devices are accounts, not `device_id`

The panel identifies a device by the credentials it signed in with. The `device`
and `device_id` fields are stored as reported but not trusted for identity:
CrossPoint (Xteink X4) sends the constants `device = "CrossPoint"` and
`device_id = "crosspoint-reader"` on every unit.

## CrossPoint (Xteink X4)

- Hashing mode defaults to filename MD5 (`FILENAME`, no Unicode normalization).
  In `SMART` mode, when no progress is found it retries with the other hash.
- Sends `Authorization: Basic` with the **plain** password alongside
  `x-auth-key`, and does not verify TLS certificates. Never log that header.
- Sends an extra `position` field and a `position_kinds` query parameter; both
  are ignored.
- For server type `OTHER`, `metadata` also carries `isbn`, `asin`, `series` and
  `series_index`; these are currently not stored.

## Storage model

- `progress_events` is append-only (an `UPDATE` trigger aborts). Every accepted
  push is one row with the receipt time.
- `progress_current` holds the latest event and the latest non-empty metadata per
  (user, document); it is updated in the same transaction as the event insert.
  Metadata belongs to the user who sent it: two users reading the same file can
  see different titles.
- `documents` is a shared registry of hashes with first-seen time only; no reading
  data lives there.
- Every write re-checks in the same transaction that the device is not revoked,
  the user is not disabled and the key has not changed, so revoking a device takes
  effect immediately.
