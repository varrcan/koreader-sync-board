// Package hashing вычисляет идентификаторы документов так же, как KOReader:
// partial MD5 (util.partialMD5) и md5 от имени файла (режим «filename» kosync).
package hashing

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const sampleSize = 1024

// sampleOffsets повторяет seek из util.partialMD5: lshift(1024, 2*i) для i = -1..10.
// При i = -1 LuaJIT bit.lshift берёт сдвиг по модулю 32 (30), и 1024<<30
// переполняет 32 бита, поэтому первое смещение 0, а не 256.
var sampleOffsets = func() []int64 {
	offs := []int64{0}
	for i := range 11 {
		offs = append(offs, sampleSize<<(2*i))
	}
	return offs
}()

// PartialMD5 возвращает partial MD5 файла (32 hex-символа в нижнем регистре).
func PartialMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return partialMD5(f)
}

func partialMD5(r io.ReaderAt) (string, error) {
	h := md5.New()
	buf := make([]byte, sampleSize)
	for _, off := range sampleOffsets {
		n, err := r.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			// Обрыв NFS/SMB или нет прав: правдоподобный, но неверный хеш хуже ошибки.
			return "", err
		}
		// Lua file:read возвращает nil, только если не прочитано ни байта.
		if n == 0 {
			break
		}
		h.Write(buf[:n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FilenameMD5Variants возвращает md5 базового имени файла (с расширением) по сырым
// байтам, в NFC и в NFD, без повторов. Копирование между ОС меняет нормализацию
// имени, а KOReader хеширует его как есть.
func FilenameMD5Variants(name string) []string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	var out []string
	for _, v := range []string{name, norm.NFC.String(name), norm.NFD.String(name)} {
		sum := md5.Sum([]byte(v))
		s := hex.EncodeToString(sum[:])
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
