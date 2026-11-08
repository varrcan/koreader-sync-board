-- Файл закреплён за книгой ручным слиянием или разделением: при изменении файла
-- сканер не выбирает книгу заново по ключу.
ALTER TABLE library_files ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
