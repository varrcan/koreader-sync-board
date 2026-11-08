-- cover — имя миниатюры в каталоге covers/. cover_checked = 1 ставится и при
-- отсутствии обложки: иначе FB2 без неё читался бы целиком на каждом скане.
ALTER TABLE library_files ADD COLUMN cover TEXT;
ALTER TABLE library_files ADD COLUMN cover_checked INTEGER NOT NULL DEFAULT 0;
