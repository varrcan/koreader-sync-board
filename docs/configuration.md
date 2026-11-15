# Configuration

English | [Русский](ru/configuration.md)

The server is configured with environment variables. `ksb serve` also accepts `-listen` and
`-data-dir`, which override the environment.

## General

| Variable | Default | Meaning |
|---|---|---|
| `KSB_LISTEN` | `:8080` | Listen address |
| `KSB_DATA_DIR` | `/data` | Data directory (SQLite database `ksb.sqlite3`, cover thumbnails in `covers/`, converted books in `converted/`, conversion work files in `tmp/`) |
| `KSB_KOSYNC_REGISTRATION` | `false` | Allow creating accounts from KOReader (`POST /users/create`) |
| `KSB_KOSYNC_SELF_DELETE` | `false` | Allow deleting an account from the device (`DELETE /users/me`) |
| `KSB_TRUSTED_PROXIES` | — | Reverse proxies whose `X-Forwarded-For` is trusted: CIDRs or single addresses, comma-separated (see [Security](security.md#behind-a-reverse-proxy)) |
| `ADMIN_USER`, `ADMIN_PASSWORD` | — | Create an admin with a panel password on first start (empty database only). Set both or neither; the password needs at least 8 characters |

## Library

| Variable | Default | Meaning |
|---|---|---|
| `KSB_LIBRARY_SCAN_INTERVAL` | `6h` | Library scan period as a Go duration (`30m`, `12h`), at least `1m`; `0` scans only when a folder is added and on demand |
| `KSB_GOOGLE_BOOKS_API_KEY` | — | Enables Google Books as a metadata catalog; the key is never logged |

## OIDC

See [Signing in with OIDC](oidc.md).

| Variable | Default | Meaning |
|---|---|---|
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
