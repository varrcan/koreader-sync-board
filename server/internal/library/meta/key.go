package meta

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Key — ключ группировки файлов в книгу: нормализованные название и авторы.
// Порядок слов в имени автора и порядок авторов не важны: FB2 даёт «Аркадий
// Стругацкий», а EPUB от calibre — «Стругацкий Аркадий». ok = false, если после
// нормализации название пустое: такой файл становится отдельной книгой.
func Key(title string, authors []string) (key string, ok bool) {
	t := Fold(title)
	if t == "" {
		return "", false
	}
	var as []string
	for _, a := range authors {
		words := strings.Fields(Fold(a))
		if len(words) == 0 {
			continue
		}
		slices.Sort(words)
		as = append(as, strings.Join(words, " "))
	}
	slices.Sort(as)
	return t + "\x1f" + strings.Join(as, "\x1e"), true
}

// Fold: NFKC, нижний регистр, ё→е, всё, кроме букв, цифр и диакритики без
// составной формы, — пробел; пробелы схлопнуты. Общая для ключа группировки и
// поиска по каталогу.
func Fold(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 'ё':
			return 'е'
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
