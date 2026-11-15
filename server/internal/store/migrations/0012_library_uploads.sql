-- managed из флага становится видом служебного корня: 1 — конвертации, 2 — загрузки.
-- По одному корню каждого вида.
DROP INDEX library_roots_managed;
CREATE UNIQUE INDEX library_roots_managed ON library_roots (managed) WHERE managed <> 0;
