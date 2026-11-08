-- Правки метаданных книги поверх данных файлов. NULL — как в файле, пустая строка —
-- значение файла скрыто. library_books остаётся материализованным итогом: его читают
-- поиск каталога и раздел прогресса.
CREATE TABLE library_book_meta (
    book_id      INTEGER PRIMARY KEY REFERENCES library_books (id) ON DELETE CASCADE,
    title        TEXT,
    authors      TEXT,
    series       TEXT,
    series_index REAL,
    language     TEXT,
    description  TEXT,
    cover        TEXT,
    source       TEXT, -- file | fantlab | openlibrary | google | NULL (вручную)
    updated_at   INTEGER NOT NULL
);

-- Нормализованные кандидаты внешних каталогов: одна запись на запрос вместо цепочки
-- ответов (поиск и детали каждого результата).
CREATE TABLE library_metadata_cache (
    provider   TEXT    NOT NULL,
    query      TEXT    NOT NULL,
    candidates TEXT    NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (provider, query)
) WITHOUT ROWID;
