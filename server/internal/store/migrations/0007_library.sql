-- Библиотека: read-only корни, файлы книг в них и книги, в которые файлы
-- сгруппированы. Корни добавляет администратор в панели.
CREATE TABLE library_roots (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    path       TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    scanned_at INTEGER,
    error      TEXT
);

-- Метаданные книги — производное от её файлов: пересчитываются при каждой записи
-- файлов книги.
CREATE TABLE library_books (
    id           INTEGER PRIMARY KEY,
    title        TEXT,
    authors      TEXT,
    series       TEXT,
    series_index REAL,
    language     TEXT,
    created_at   INTEGER NOT NULL
);

-- Ключ группировки → книга. Ключи отдельно от книги, чтобы ручное слияние (у книги
-- несколько ключей) пережило пересканирование.
CREATE TABLE library_book_keys (
    key     TEXT    PRIMARY KEY,
    book_id INTEGER NOT NULL REFERENCES library_books (id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX library_book_keys_book ON library_book_keys (book_id);

-- book_id без CASCADE: книга без файлов удаляется явной уборкой в той же
-- транзакции, что изменила файлы.
CREATE TABLE library_files (
    id           INTEGER PRIMARY KEY,
    root_id      INTEGER NOT NULL REFERENCES library_roots (id) ON DELETE CASCADE,
    path         TEXT    NOT NULL, -- относительно корня, через «/»
    size         INTEGER NOT NULL,
    mtime_ns     INTEGER NOT NULL,
    format       TEXT    NOT NULL, -- epub | fb2 | fb2.zip | pdf | …
    partial_md5  TEXT    NOT NULL,
    book_id      INTEGER NOT NULL REFERENCES library_books (id),
    title        TEXT,
    authors      TEXT,
    series       TEXT,
    series_index REAL,
    language     TEXT,
    meta_error   TEXT,
    indexed_at   INTEGER NOT NULL,
    UNIQUE (root_id, path)
);
CREATE INDEX library_files_book ON library_files (book_id);

-- Все хеши файла: partial MD5 и md5 имени в вариантах raw/NFC/NFD. Не ссылаются на
-- documents: реестр документов — хеши от читалок, библиотека его не раздувает.
CREATE TABLE library_file_hashes (
    hash    TEXT    NOT NULL,
    file_id INTEGER NOT NULL REFERENCES library_files (id) ON DELETE CASCADE,
    PRIMARY KEY (hash, file_id)
) WITHOUT ROWID;
CREATE INDEX library_file_hashes_file ON library_file_hashes (file_id);
