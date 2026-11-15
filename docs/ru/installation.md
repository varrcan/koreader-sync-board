# Установка

[English](../installation.md) | Русский

- [Docker](#docker)
- [Бинарник](#бинарник)
- [systemd](#systemd)
- [Первый запуск](#первый-запуск)

## Docker

```sh
docker run -d --name ksb \
  -v ksb-data:/data \
  -p 8080:8080 \
  ghcr.io/varrcan/koreader-sync-board:latest
```

Образы для linux/amd64 и linux/arm64 помечены версией релиза (`1.2.3`, `1.2`) и `latest`. Чтобы
собрать образ самостоятельно, выполните в репозитории `docker build -t koreader-sync-board .`.

Используйте **именованный том** (`ksb-data` выше): при первом использовании Docker сам назначит
ему нужного владельца. Контейнер работает от непривилегированного пользователя `65532`. При bind
mount сначала дайте этому пользователю право записи в каталог хоста:

```sh
mkdir -p ./data && sudo chown 65532:65532 ./data
docker run -d --name ksb -v "$PWD/data:/data" -p 8080:8080 \
  ghcr.io/varrcan/koreader-sync-board:latest
```

В образ встроена проверка здоровья (`ksb healthcheck`, запрос к `/api/v1/health`). Командная
строка `ksb` доступна внутри контейнера как `docker exec ksb /ksb …`.

## Бинарник

`ksb` — один статический бинарник со встроенной панелью: для работы не нужны ни Docker, ни Node,
только каталог данных с правом записи. Скачайте архив для своей архитектуры (`linux_amd64` или
`linux_arm64`) из [Releases](https://github.com/varrcan/koreader-sync-board/releases) и сверьте
его с `SHA256SUMS`:

```sh
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf ksb_v1.2.3_linux_amd64.tar.gz
KSB_DATA_DIR=./data ./ksb serve
```

Настройки — те же переменные окружения, что и в Docker (см. [Настройка](configuration.md)), но
каталог данных по умолчанию — `/data`, поэтому задайте `KSB_DATA_DIR`. Команды `ksb user …` и
`ksb device …` открывают базу напрямую: запускайте их с тем же `KSB_DATA_DIR` и от того же
системного пользователя, что и сервер. Там, где в документации написано `docker exec ksb /ksb`,
вызывайте `ksb` напрямую.

### Сборка из исходников

Требования: Go 1.27, Node 22+ с pnpm.

```sh
pnpm install --frozen-lockfile
pnpm -F web build
CGO_ENABLED=0 go -C server build -trimpath -tags webdist -o ../bin/ksb ./cmd/ksb
```

или `task build` через [Task](https://taskfile.dev). Без тега `webdist` вместо панели будет
страница-заглушка. Для другой машины добавьте `GOOS=linux GOARCH=arm64` (или `amd64`): бинарник
без CGO и кросс-компилируется как есть.

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
# Остальные настройки: по одной KSB_…=… на строку.
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

`ProtectSystem=strict` оставляет систему доступной только для чтения, кроме каталога данных;
каталогам [библиотеки](library.md) этого достаточно — сервер их только читает. Пользователю
`ksb` нужно право чтения на них.

## Первый запуск

Откройте `http://<хост>:8080`. На новом сервере без пользователей панель показывает **мастер
первого запуска**: он просит одноразовый код настройки, который сервер пишет в лог при старте.
Найти его:

```sh
docker logs ksb 2>&1 | grep setup_code          # Docker
journalctl -u ksb | grep setup_code             # бинарник под systemd (ksb.service)
```

Введите код (регистр, пробелы и дефисы не важны), выберите имя пользователя и пароль панели
длиной от 8 до 256 символов: так создаётся администратор, и вы сразу входите в панель. Код
хранится только в памяти, поэтому после перезапуска ищите самую свежую строку. Чтобы обойтись
без мастера, задайте `ADMIN_USER` и `ADMIN_PASSWORD` (обе или ни одной) до первого запуска.

Дальше: [добавьте устройство и подключите KOReader](koreader.md).
