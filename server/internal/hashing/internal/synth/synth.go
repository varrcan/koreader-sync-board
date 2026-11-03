// Package synth создаёт синтетические файлы с детерминированным содержимым для
// эталонных тестов partial MD5. Им пользуются и генератор golden.json, и тесты.
package synth

import (
	"fmt"
	"os"
)

// Case — один синтетический файл.
type Case struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Sparse bool   `json:"sparse,omitempty"`
	MD5    string `json:"md5"`
}

// SampleOffsets — смещения сэмплов KOReader: 0, затем 1024 << 2i для i = 0..10.
func SampleOffsets() []int64 {
	offs := []int64{0}
	for i := range 11 {
		offs = append(offs, int64(1024)<<(2*i))
	}
	return offs
}

// Cases возвращает набор граничных размеров. Плотные файлы — до 1 MiB, плюс
// разреженные больше 1 GiB, где в хеш попадают все 12 сэмплов.
func Cases() []Case {
	seen := map[int64]bool{}
	var cs []Case
	add := func(size int64, sparse bool) {
		if size < 0 || (!sparse && seen[size]) {
			return
		}
		name := fmt.Sprintf("size_%d", size)
		if sparse {
			name = "sparse_" + name
		} else {
			seen[size] = true
		}
		cs = append(cs, Case{Name: name, Size: size, Sparse: sparse})
	}
	add(0, false)
	add(1, false)
	for _, off := range SampleOffsets() {
		if off > 1<<20 {
			break
		}
		for _, d := range []int64{-1, 0, 1, 1023, 1024, 1025} {
			add(off+d, false)
		}
	}
	last := int64(1 << 30)
	for _, d := range []int64{1, 1023, 1024, 4096} {
		add(last+d, true)
	}
	return cs
}

// Byte — детерминированное содержимое файла в позиции pos.
func Byte(pos int64) byte {
	x := uint64(pos/8) + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return byte(x >> (8 * uint(pos%8)))
}

// Fill заполняет buf содержимым, начиная с позиции pos.
func Fill(buf []byte, pos int64) {
	for i := range buf {
		buf[i] = Byte(pos + int64(i))
	}
}

// Write создаёт файл по описанию c. Плотный файл заполняется целиком, у
// разреженного данные пишутся только в окна сэмплов, остальное — дыры.
func Write(path string, c Case) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f, c); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func write(f *os.File, c Case) error {
	if c.Sparse {
		if err := f.Truncate(c.Size); err != nil {
			return err
		}
		for _, off := range SampleOffsets() {
			n := min(int64(1024), c.Size-off)
			if n <= 0 {
				break
			}
			buf := make([]byte, n)
			Fill(buf, off)
			if _, err := f.WriteAt(buf, off); err != nil {
				return err
			}
		}
		return nil
	}
	buf := make([]byte, 64*1024)
	for pos := int64(0); pos < c.Size; {
		n := min(int64(len(buf)), c.Size-pos)
		Fill(buf[:n], pos)
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		pos += n
	}
	return nil
}
