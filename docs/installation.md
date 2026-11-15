# Installation

English | [Русский](ru/installation.md)

- [Docker](#docker)
- [Binary](#binary)
- [systemd](#systemd)
- [First run](#first-run)

## Docker

```sh
docker run -d --name ksb \
  -v ksb-data:/data \
  -p 8080:8080 \
  ghcr.io/varrcan/koreader-sync-board:latest
```

Images for linux/amd64 and linux/arm64 are tagged by release (`1.2.3`, `1.2`) and `latest`. To
build the image yourself, run `docker build -t koreader-sync-board .` in the repository.

Use a **named volume** (`ksb-data` above): Docker gives it the right owner on first use. The
container runs as the unprivileged user `65532`. With a bind mount, make the host directory
writable for that user first:

```sh
mkdir -p ./data && sudo chown 65532:65532 ./data
docker run -d --name ksb -v "$PWD/data:/data" -p 8080:8080 \
  ghcr.io/varrcan/koreader-sync-board:latest
```

The image has a built-in health check (`ksb healthcheck`, which requests `/api/v1/health`). The
`ksb` command line is available inside the container as `docker exec ksb /ksb …`.

## Binary

`ksb` is a single static binary with the panel embedded: it needs neither Docker nor Node at
runtime, only a writable data directory. Download the archive for your architecture
(`linux_amd64` or `linux_arm64`) from
[Releases](https://github.com/varrcan/koreader-sync-board/releases) and check it against
`SHA256SUMS`:

```sh
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf ksb_v1.2.3_linux_amd64.tar.gz
KSB_DATA_DIR=./data ./ksb serve
```

The settings are the same environment variables as in Docker (see
[Configuration](configuration.md)), but the default data directory is `/data`, so set
`KSB_DATA_DIR`. The `ksb user …` and `ksb device …` commands open the database directly: run
them with the same `KSB_DATA_DIR` and as the same system user as the server. Wherever the
documentation says `docker exec ksb /ksb`, use that instead.

### Building from source

Requirements: Go 1.27, Node 22+ with pnpm.

```sh
pnpm install --frozen-lockfile
pnpm -F web build
CGO_ENABLED=0 go -C server build -trimpath -tags webdist -o ../bin/ksb ./cmd/ksb
```

or `task build` with [Task](https://taskfile.dev). Without the `webdist` tag the panel is
replaced by a placeholder page. For another machine add `GOOS=linux GOARCH=arm64` (or `amd64`):
the binary has no CGO and cross-compiles as is.

## systemd

```sh
sudo install -m 755 ksb /usr/local/bin/ksb
sudo useradd --system --home-dir /var/lib/ksb --shell /usr/sbin/nologin ksb
```

`/etc/systemd/system/ksb.service`:

```ini
[Unit]
Description=koreader-sync-board
After=network-online.target
Wants=network-online.target

[Service]
User=ksb
Group=ksb
ExecStart=/usr/local/bin/ksb serve
Environment=KSB_DATA_DIR=/var/lib/ksb
Environment=KSB_LISTEN=:8080
# Other settings: one KSB_…=… per line.
EnvironmentFile=-/etc/ksb.env
StateDirectory=ksb
StateDirectoryMode=0750
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now ksb
sudo -u ksb KSB_DATA_DIR=/var/lib/ksb ksb user list
```

`ProtectSystem=strict` leaves the system read-only except the data directory, which suits
[library](library.md) folders: the server only reads them. The `ksb` user needs read access to
them.

## First run

Open `http://<host>:8080`. On a fresh server with no users the panel shows the **setup
wizard**: it asks for a one-time setup code that the server writes to its log on startup. Find
it with

```sh
docker logs ksb 2>&1 | grep setup_code          # Docker
journalctl -u ksb | grep setup_code             # binary under systemd (ksb.service)
```

Enter the code (case, spaces and dashes do not matter), choose a username and a panel password
of 8 to 256 characters: this creates the administrator and signs you in. The code lives only in
memory, so after a restart look for the newest line. To skip the wizard, set `ADMIN_USER` and
`ADMIN_PASSWORD` (both, or neither) before the first start.

Next: [add a device and connect KOReader](koreader.md).
