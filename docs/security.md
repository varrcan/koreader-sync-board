# Security and reverse proxy

English | [Русский](ru/security.md)

- [Failed logins](#failed-logins)
- [Behind a reverse proxy](#behind-a-reverse-proxy)
- [fail2ban](#fail2ban)

## Failed logins

After **10 failed logins within 10 minutes** from one client address, that address is blocked
for **15 minutes**. KOReader logins (progress sync, WebDAV and OPDS), panel sign-ins (a wrong
password, including the current password when changing it) and wrong setup codes share this
budget. IPv6 clients are counted per `/64` network. Counters live in memory and reset on
restart.

- KOReader shows the reason: *"Too many failed attempts. Try again in N minutes."* (status 401,
  code 2001, plus a `Retry-After` header for other clients). A blocked `POST /users/create` gets
  402/2005 with a similar text.
- A device that has logged in recently keeps syncing from a blocked address: the server
  remembers successful checks, and every request extends that.
- Sending the **same** wrong password again (for example, a forgotten e-reader whose device was
  revoked) is answered at once and does not count towards the limit, so it cannot lock out the
  other e-readers in the same home network.
- With registration enabled, every `POST /users/create` counts as an attempt, successful ones
  too.
- The panel answers a blocked address with `429 Too Many Requests` and `Retry-After`. Unlike
  KOReader, a repeated wrong panel password counts every time.
- Failed OIDC sign-ins do not count and are logged as `panel: oidc login failed` with a
  `reason`, not as `authorization failed`: a refusal at the provider is not password guessing.

## Behind a reverse proxy

Progress sync, WebDAV and OPDS send the device password with every request (OPDS and WebDAV as
HTTP Basic), so outside the home network serve them over HTTPS through a reverse proxy.

Behind nginx, nginx proxy manager, Caddy or Traefik every request comes from the proxy's
address. Tell the server which addresses are your proxy, so that it takes the client address
from `X-Forwarded-For`:

```sh
docker run ... -e KSB_TRUSTED_PROXIES=172.17.0.0/16 ghcr.io/varrcan/koreader-sync-board:latest
```

List **only the proxy itself** (its address, or the Docker network it reaches the server from).
The header is read right to left, and the first address that is not a trusted proxy is the
client; for a request from a non-trusted address the header is ignored. The proxy must append
the client address, as nginx does with
`proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` (nginx proxy manager does this by
default).

Getting `KSB_TRUSTED_PROXIES` wrong has consequences either way:

- **not set** behind a proxy: all clients share the proxy's address, so one attacker blocks
  logins for everyone whose device has not logged in recently, and the log shows only the proxy;
- **too broad** (for example `0.0.0.0/0`): any client can put an arbitrary address into
  `X-Forwarded-For` and bypass the limit.

The proxy must also:

- pass the original `Host` header (nginx: `proxy_set_header Host $host;`; nginx proxy manager,
  Caddy and Traefik do this by default). The panel rejects sign-in and other changes from a
  foreign site; over plain `http://` browsers do not send `Sec-Fetch-Site`, so the server
  compares `Origin` with `Host`, and a rewritten `Host` makes every sign-in fail with
  `cross_origin`;
- keep the `ETag` response header and the `If-Match` request header on `/dav/` and allow request
  bodies up to 64 MiB (nginx: `client_max_body_size 64m;`). Without `ETag` KOReader uploads
  without `If-Match`, gets `412` and retries forever.

## fail2ban

Every failed login is logged as one JSON line with the client address:

```json
{"time":"2026-11-04T21:47:10.84+03:00","level":"WARN","msg":"kosync: authorization failed","user":"alice","ip":"198.51.100.7","path":"/users/auth","reason":"unauthorized"}
{"time":"2026-11-04T21:48:02.11+03:00","level":"WARN","msg":"panel: authorization failed","user":"alice","ip":"198.51.100.7","path":"/api/v1/auth/login","reason":"unauthorized"}
{"time":"2026-11-04T21:49:31.52+03:00","level":"WARN","msg":"webdav: authorization failed","user":"alice","ip":"198.51.100.7","path":"/dav/statistics.sqlite3","reason":"unauthorized"}
{"time":"2026-11-04T21:50:12.03+03:00","level":"WARN","msg":"opds: authorization failed","user":"alice","ip":"198.51.100.7","path":"/opds","reason":"unauthorized"}
```

Repeats of an already rejected password, requests refused because of a block and OPDS requests
without any password (many clients try that first) are not logged. To ban persistent offenders
for longer, create `/etc/fail2ban/filter.d/ksb.conf`:

```ini
[Definition]
# Matches the plain JSON line and Docker's json-file log, where quotes are escaped.
failregex = \\?"msg\\?":\\?"(?:kosync|panel|webdav|opds): authorization failed\\?".*\\?"ip\\?":\\?"<HOST>\\?",\\?"path\\?"
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

Ban on the host that receives the client connections: with a reverse proxy, that is the proxy's
host. Check the filter against your log with
`fail2ban-regex /var/lib/docker/containers/<id>/<id>-json.log /etc/fail2ban/filter.d/ksb.conf`.
