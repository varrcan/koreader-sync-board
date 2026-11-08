// Package config читает настройки сервера из переменных окружения.
package config

import (
	"cmp"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

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

	// OIDC — вход в панель через OIDC-провайдера; nil, если не настроен.
	OIDC *OIDC
	// PanelPasswordLogin — вход по паролю панели для пользователей с привязанным
	// OIDC (KSB_PANEL_PASSWORD_LOGIN, по умолчанию true). Непривязанные входят
	// паролем всегда.
	PanelPasswordLogin bool

	// LibraryScanInterval — период скана библиотеки (KSB_LIBRARY_SCAN_INTERVAL,
	// по умолчанию 6h); 0 — только вручную и при добавлении корня.
	LibraryScanInterval time.Duration

	// GoogleBooksAPIKey включает провайдера метаданных Google Books
	// (KSB_GOOGLE_BOOKS_API_KEY); пусто — выключен.
	GoogleBooksAPIKey string
}

// OIDC — настройки OIDC-провайдера панели.
type OIDC struct {
	Issuer       string // KSB_OIDC_ISSUER
	ClientID     string // KSB_OIDC_CLIENT_ID
	ClientSecret string // KSB_OIDC_CLIENT_SECRET; пусто — публичный клиент только с PKCE
	Name         string // KSB_OIDC_NAME — подпись кнопки входа
	// Scopes всегда содержит openid (KSB_OIDC_SCOPES, через пробел).
	Scopes        []string
	UsernameClaim string // KSB_OIDC_USERNAME_CLAIM
	AutoCreate    bool   // KSB_OIDC_AUTO_CREATE
	// AdminClaim и AdminValue задаются только вместе; пустые — роль по claim не синхронизируется.
	AdminClaim string // KSB_OIDC_ADMIN_CLAIM
	AdminValue string // KSB_OIDC_ADMIN_VALUE
	// PublicURL — origin панели из KSB_PUBLIC_URL без завершающего «/».
	PublicURL string
}

// RedirectURL — адрес возврата с провайдера, зарегистрированный в IdP.
func (o *OIDC) RedirectURL() string {
	return o.PublicURL + "/api/v1/auth/oidc/callback"
}

// Defaults возвращает значения по умолчанию.
func Defaults() Config {
	return Config{
		Listen:  ":8080",
		DataDir: "/data",

		PanelPasswordLogin: true,

		LibraryScanInterval: 6 * time.Hour,
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
	// Одна заданная переменная — почти наверняка опечатка в имени другой. Молча
	// пропустить bootstrap значило бы открыть мастер первого запуска.
	switch {
	case c.AdminUser != "" && c.AdminPassword == "":
		return Config{}, fmt.Errorf("ADMIN_USER is set but ADMIN_PASSWORD is not: set both or neither")
	case c.AdminUser == "" && c.AdminPassword != "":
		return Config{}, fmt.Errorf("ADMIN_PASSWORD is set but ADMIN_USER is not: set both or neither")
	}
	if c.OIDC, err = loadOIDC(getenv); err != nil {
		return Config{}, err
	}
	if v := getenv("KSB_PANEL_PASSWORD_LOGIN"); v != "" {
		if c.PanelPasswordLogin, err = strconv.ParseBool(v); err != nil {
			return Config{}, fmt.Errorf("KSB_PANEL_PASSWORD_LOGIN: invalid boolean %q", v)
		}
	}
	// Без OIDC выключенный вход по паролю запер бы панель целиком.
	if !c.PanelPasswordLogin && c.OIDC == nil {
		return Config{}, fmt.Errorf("KSB_PANEL_PASSWORD_LOGIN=false requires OIDC: set KSB_OIDC_ISSUER, KSB_OIDC_CLIENT_ID and KSB_PUBLIC_URL")
	}
	if v := getenv("KSB_LIBRARY_SCAN_INTERVAL"); v != "" {
		if c.LibraryScanInterval, err = parseScanInterval(v); err != nil {
			return Config{}, fmt.Errorf("KSB_LIBRARY_SCAN_INTERVAL: %w", err)
		}
	}
	c.GoogleBooksAPIKey = getenv("KSB_GOOGLE_BOOKS_API_KEY")
	return c, nil
}

// parseScanInterval разбирает период скана библиотеки. Период меньше минуты,
// кроме 0, — почти наверняка опечатка: частый обход NAS бессмыслен.
func parseScanInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	switch {
	case err != nil:
		return 0, fmt.Errorf("invalid duration %q (use Go duration format: 30m, 6h; 0 disables)", v)
	case d < 0:
		return 0, fmt.Errorf("%q must not be negative", v)
	case d > 0 && d < time.Minute:
		return 0, fmt.Errorf("%q: period must be at least one minute (0 disables periodic scans)", v)
	}
	return d, nil
}

// loadOIDC читает KSB_OIDC_* и KSB_PUBLIC_URL; без всех трёх обязательных — nil.
func loadOIDC(getenv func(string) string) (*OIDC, error) {
	o := &OIDC{
		Issuer:        getenv("KSB_OIDC_ISSUER"),
		ClientID:      getenv("KSB_OIDC_CLIENT_ID"),
		ClientSecret:  getenv("KSB_OIDC_CLIENT_SECRET"),
		Name:          cmp.Or(getenv("KSB_OIDC_NAME"), "SSO"),
		UsernameClaim: cmp.Or(getenv("KSB_OIDC_USERNAME_CLAIM"), "preferred_username"),
		AdminClaim:    getenv("KSB_OIDC_ADMIN_CLAIM"),
		AdminValue:    getenv("KSB_OIDC_ADMIN_VALUE"),
	}
	switch {
	case o.AdminClaim != "" && o.AdminValue == "":
		return nil, fmt.Errorf("KSB_OIDC_ADMIN_CLAIM is set but KSB_OIDC_ADMIN_VALUE is not: set both or neither")
	case o.AdminClaim == "" && o.AdminValue != "":
		return nil, fmt.Errorf("KSB_OIDC_ADMIN_VALUE is set but KSB_OIDC_ADMIN_CLAIM is not: set both or neither")
	}
	publicURL := getenv("KSB_PUBLIC_URL")
	required := []struct{ name, value string }{
		{"KSB_OIDC_ISSUER", o.Issuer}, {"KSB_OIDC_CLIENT_ID", o.ClientID}, {"KSB_PUBLIC_URL", publicURL},
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	switch len(missing) {
	case len(required):
		return nil, nil
	case 0:
	default:
		return nil, fmt.Errorf("OIDC requires KSB_OIDC_ISSUER, KSB_OIDC_CLIENT_ID and KSB_PUBLIC_URL: %s is not set",
			strings.Join(missing, ", "))
	}
	var err error
	if o.PublicURL, err = parseOrigin(publicURL); err != nil {
		return nil, fmt.Errorf("KSB_PUBLIC_URL: %w", err)
	}
	if o.AutoCreate, err = parseBool(getenv, "KSB_OIDC_AUTO_CREATE"); err != nil {
		return nil, err
	}
	o.Scopes = strings.Fields(cmp.Or(getenv("KSB_OIDC_SCOPES"), "openid profile email"))
	if !slices.Contains(o.Scopes, "openid") {
		o.Scopes = append([]string{"openid"}, o.Scopes...)
	}
	return o, nil
}

// parseOrigin проверяет, что v — абсолютный http(s) URL без пути, query и
// фрагмента, и возвращает его без завершающего «/». Путь запрещён: панель за
// префиксом не поддерживается — cookie потока и редиректы идут от корня.
func parseOrigin(v string) (string, error) {
	u, err := url.Parse(v)
	switch {
	case err != nil:
		return "", fmt.Errorf("invalid URL %q", v)
	case (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return "", fmt.Errorf("%q must be an absolute http or https URL", v)
	case u.Path != "" && u.Path != "/":
		return "", fmt.Errorf("%q must not have a path: serving the panel under a prefix is not supported", v)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(v, "#") || u.User != nil:
		return "", fmt.Errorf("%q must be an origin without query, fragment or credentials", v)
	}
	// Origin в том виде, в каком его отдаёт браузер (window.location.origin) и
	// присылает в Host: хост в нижнем регистре, порт по умолчанию опущен. Иначе
	// «https://host:443» никогда не совпал бы с Host, и вход по OIDC зациклился
	// бы на перенаправлении на публичный адрес.
	host := strings.ToLower(u.Host)
	if p := u.Port(); (u.Scheme == "http" && p == "80") || (u.Scheme == "https" && p == "443") {
		host = strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	return u.Scheme + "://" + host, nil
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
