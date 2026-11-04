// Package config читает настройки сервера из переменных окружения.
package config

import (
	"fmt"
	"net/netip"
	"strconv"

	"github.com/varrcan/koreader-sync-board/server/internal/clientip"
)

// Config — настройки сервера. Флаги командной строки перекрывают значения из окружения.
type Config struct {
	Listen  string // KSB_LISTEN
	DataDir string // KSB_DATA_DIR

	// KosyncRegistration разрешает POST /users/create (KSB_KOSYNC_REGISTRATION).
	KosyncRegistration bool
	// KosyncSelfDelete разрешает DELETE /users/me (KSB_KOSYNC_SELF_DELETE).
	KosyncSelfDelete bool

	// TrustedProxies — прокси, которым верим X-Forwarded-For (KSB_TRUSTED_PROXIES).
	TrustedProxies []netip.Prefix

	// AdminUser и AdminPassword создают администратора при пустой базе.
	AdminUser     string // ADMIN_USER
	AdminPassword string // ADMIN_PASSWORD
}

// Defaults возвращает значения по умолчанию.
func Defaults() Config {
	return Config{
		Listen:  ":8080",
		DataDir: "/data",
	}
}

// Load читает конфигурацию через getenv (обычно os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	c := Defaults()
	if v := getenv("KSB_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := getenv("KSB_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	var err error
	if c.KosyncRegistration, err = parseBool(getenv, "KSB_KOSYNC_REGISTRATION"); err != nil {
		return Config{}, err
	}
	if c.KosyncSelfDelete, err = parseBool(getenv, "KSB_KOSYNC_SELF_DELETE"); err != nil {
		return Config{}, err
	}
	if c.TrustedProxies, err = clientip.ParsePrefixes(getenv("KSB_TRUSTED_PROXIES")); err != nil {
		return Config{}, fmt.Errorf("KSB_TRUSTED_PROXIES: %w", err)
	}
	c.AdminUser = getenv("ADMIN_USER")
	c.AdminPassword = getenv("ADMIN_PASSWORD")
	return c, nil
}

func parseBool(getenv func(string) string, name string) (bool, error) {
	v := getenv(name)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", name, v)
	}
	return b, nil
}
