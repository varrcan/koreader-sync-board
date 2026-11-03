package accounts

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params — параметры argon2id.
type Argon2Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

var (
	// DefaultArgon2Params — минимальная рекомендация OWASP: m=19 MiB, t=2, p=1.
	DefaultArgon2Params = Argon2Params{MemoryKiB: 19 * 1024, Time: 2, Threads: 1}
	// TestArgon2Params — дешёвые параметры для тестов.
	TestArgon2Params = Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}
)

const (
	saltLen = 16
	keyLen  = 32
)

var b64 = base64.RawStdEncoding

// hashSecret возвращает хеш в формате PHC: $argon2id$v=19$m=..,t=..,p=..$salt$hash.
func hashSecret(secret string, p Argon2Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(secret), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

var errBadHash = errors.New("malformed argon2id hash")

// verifySecret сверяет secret с хешем; параметры берутся из самого хеша.
func verifySecret(secret, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return false, errBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(secret), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
