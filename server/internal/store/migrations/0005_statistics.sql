-- Файлы облачной синхронизации KOReader (WebDAV): ровно принятые байты. Снимок
-- хранится в базе, чтобы запись файла и замена разобранной статистики шли в одной
-- транзакции. credential_id — устройство, выгрузившее файл; NULL для загрузки из
-- панели (source = 'import').
CREATE TABLE webdav_files (
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    content       BLOB    NOT NULL,
    etag          TEXT    NOT NULL,
    size          INTEGER NOT NULL,
    modified_at   INTEGER NOT NULL,
    credential_id INTEGER REFERENCES device_credentials (id) ON DELETE SET NULL,
    source        TEXT    NOT NULL CHECK (source IN ('webdav', 'import')),
    UNIQUE (user_id, name)
);

-- Книги из последнего принятого statistics.sqlite3 (таблица book KOReader).
-- Исходный id книги из файла не хранится. document_id — NULL у книги без md5.
CREATE TABLE stat_books (
    id               INTEGER PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    document_id      INTEGER REFERENCES documents (id),
    title            TEXT,
    authors          TEXT,
    series           TEXT,
    language         TEXT,
    pages            INTEGER,
    last_open        INTEGER,
    notes            INTEGER,
    highlights       INTEGER,
    total_read_time  INTEGER,
    total_read_pages INTEGER
);
CREATE INDEX stat_books_user_document ON stat_books (user_id, document_id);

-- Записи чтения страниц (page_stat_data). user_id дублируется, чтобы выборки по
-- пользователю и времени шли без соединения с книгами. Индекс по book_id нужен
-- каскадному удалению: без него каждая удалённая книга просматривает всю таблицу.
CREATE TABLE stat_pages (
    book_id     INTEGER NOT NULL REFERENCES stat_books (id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL,
    page        INTEGER NOT NULL,
    start_time  INTEGER NOT NULL,
    duration    INTEGER NOT NULL,
    total_pages INTEGER NOT NULL
);
CREATE INDEX stat_pages_book ON stat_pages (book_id);
CREATE INDEX stat_pages_user_time ON stat_pages (user_id, start_time);
