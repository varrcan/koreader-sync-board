-- Книги пользователя от последней синхронизации: сортировка и курсор списка книг
-- по event_id. Ключ таблицы (user_id, document_id) этого порядка не даёт.
CREATE INDEX progress_current_recent ON progress_current (user_id, event_id);
