-- managed = 1 — служебный корень сконвертированных файлов в каталоге данных. Строка в
-- library_roots, а не NULL в root_id: иначе каждому JOIN понадобилась бы своя ветка.
ALTER TABLE library_roots ADD COLUMN managed INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX library_roots_managed ON library_roots (managed) WHERE managed = 1;

-- source_md5 — partial MD5 исходного файла, а не его id: id меняется при переименовании
-- и пересоздании корня, и тогда появился бы второй EPUB с другим хешем.
ALTER TABLE library_files ADD COLUMN source_md5 TEXT;
CREATE UNIQUE INDEX library_files_source ON library_files (source_md5) WHERE source_md5 IS NOT NULL;
