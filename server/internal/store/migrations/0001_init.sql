-- Пользователи. Пароль панели отделён от ключей устройств и в kosync не работает.
CREATE TABLE users (
    id                  INTEGER PRIMARY KEY,
    username            TEXT    NOT NULL UNIQUE,
    role                TEXT    NOT NULL CHECK (role IN ('admin', 'user')),
    panel_password_hash TEXT,
    created_at          INTEGER NOT NULL,
    disabled_at         INTEGER
);

-- Учётные данные устройств. key_hash = argon2id(md5(пароль устройства)), открытый
-- ключ не хранится. key_rev растёт при каждой смене ключа, чтобы закешированные
-- проверки старого ключа переставали действовать.
CREATE TABLE device_credentials (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    key_hash     TEXT    NOT NULL,
    key_rev      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
);
CREATE INDEX device_credentials_user ON device_credentials (user_id);
-- Имя устройства уникально среди действующих; после отзыва его можно занять снова.
CREATE UNIQUE INDEX device_credentials_active_name ON device_credentials (user_id, name)
    WHERE revoked_at IS NULL;

-- Общий для инстанса реестр документов: только хеш и время первого появления.
-- Данных чтения конкретных пользователей здесь нет.
CREATE TABLE documents (
    id            INTEGER PRIMARY KEY,
    hash          TEXT    NOT NULL UNIQUE,
    first_seen_at INTEGER NOT NULL
);

-- Журнал прогресса: только INSERT. server_ts — время получения запроса.
CREATE TABLE progress_events (
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    document_id   INTEGER NOT NULL REFERENCES documents (id),
    credential_id INTEGER REFERENCES device_credentials (id) ON DELETE SET NULL,
    percentage    REAL    NOT NULL,
    progress      TEXT    NOT NULL,
    device        TEXT    NOT NULL,
    device_id     TEXT,
    meta_json     TEXT,
    server_ts     INTEGER NOT NULL
);
CREATE INDEX progress_events_user_document ON progress_events (user_id, document_id, id);

CREATE TRIGGER progress_events_append_only
    BEFORE UPDATE ON progress_events
BEGIN
    SELECT RAISE(ABORT, 'progress_events is append-only');
END;

-- Текущее состояние на пару (пользователь, документ): производное от журнала,
-- event_id всегда указывает на последнее событие пары. Метаданные из запросов
-- у каждого пользователя свои.
CREATE TABLE progress_current (
    user_id         INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    document_id     INTEGER NOT NULL REFERENCES documents (id),
    event_id        INTEGER NOT NULL REFERENCES progress_events (id) ON DELETE CASCADE,
    meta_filename   TEXT,
    meta_title      TEXT,
    meta_authors    TEXT,
    meta_updated_at INTEGER,
    PRIMARY KEY (user_id, document_id)
) WITHOUT ROWID;
