-- Сессии панели. token_hash = sha256(токен из cookie): токен случайный (256 бит),
-- поэтому медленный хеш не нужен, а утечка базы не даёт войти.
CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   BLOB    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);
