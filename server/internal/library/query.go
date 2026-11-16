package library

import (
	"path/filepath"
	"strings"
)

// maxQueryLength — предел текста запроса к внешним каталогам, как у ручного запроса
// в панели. Фон и диалог строят запрос одной функцией и попадают в один ключ кеша.
const maxQueryLength = 200

// AutoQuery — текст запроса: название и авторы, а без названия — имя файла без расширения.
func AutoQuery(b Book) string {
	var text string
	if b.Title != nil {
		text = *b.Title
		if b.Authors != nil {
			text += " " + *b.Authors
		}
	} else {
		text = strings.TrimSuffix(b.Filename, ".fb2.zip")
		if text == b.Filename {
			text = strings.TrimSuffix(b.Filename, filepath.Ext(b.Filename))
		}
	}
	if r := []rune(text); len(r) > maxQueryLength {
		text = string(r[:maxQueryLength])
	}
	return strings.TrimSpace(text)
}

// BookLanguage — первые две буквы языка книги. Поле свободное (встречается «рус»),
// а каталогам нужен код: не-ASCII значение кодом не считается.
func BookLanguage(b Book) string {
	if b.Language == nil || len(*b.Language) < 2 {
		return ""
	}
	code := strings.ToLower((*b.Language)[:2])
	for _, c := range []byte(code) {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return code
}
