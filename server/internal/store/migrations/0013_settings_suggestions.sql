-- Настройки сервера, которые администратор меняет в панели. Значение по умолчанию —
-- в коде: отсутствующий ключ означает его.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

-- Состояние фонового поиска метаданных книги; нет строки — книга не проверялась.
-- Отдельно от library_books: строки книг пересчитывает refreshBooks, а к метаданным
-- книги это состояние не относится.
CREATE TABLE library_book_suggestions (
    book_id    INTEGER PRIMARY KEY REFERENCES library_books (id) ON DELETE CASCADE,
    state      TEXT    NOT NULL, -- complete | none | found | failed | dismissed
    checked_at INTEGER NOT NULL
);
