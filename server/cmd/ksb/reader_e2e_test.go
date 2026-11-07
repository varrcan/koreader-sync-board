package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/config"
)

// reader притворяется KOReader: заголовки и тела запросов — как в
// kosync.koplugin/KOSyncClient.lua. Сервер доверяет 127.0.0.1, поэтому читалки
// с разных адресов различаются через X-Forwarded-For.
type reader struct {
	t        *testing.T
	base     string
	ip       string
	user     string
	password string
}

type answer struct {
	status     int
	retryAfter string
	body       map[string]any
}

func (r reader) call(method, path string, body any) answer {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.base+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.koreader.v1+json")
	req.Header.Set("x-auth-user", r.user)
	req.Header.Set("x-auth-key", accounts.KeyFromPassword(r.password))
	req.Header.Set("X-Forwarded-For", r.ip)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	a := answer{status: res.StatusCode, retryAfter: res.Header.Get("Retry-After")}
	if err := json.NewDecoder(res.Body).Decode(&a.body); err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	return a
}

func (r reader) login() answer { return r.call("GET", "/users/auth", nil) }

func (r reader) push(doc string, pct float64) answer {
	return r.call("PUT", "/syncs/progress", map[string]any{
		"document": doc, "progress": "/body/DocFragment[5]/body/p[3]/text().0",
		"percentage": pct, "device": "e2e", "device_id": "E2E",
	})
}

func (r reader) pull(doc string) answer { return r.call("GET", "/syncs/progress/"+doc, nil) }

func (a answer) expect(t *testing.T, name string, status int, message string) {
	t.Helper()
	if a.status != status || (message != "" && a.body["message"] != message) {
		t.Fatalf("%s: %d %v, want %d %q", name, a.status, a.body, status, message)
	}
}

// syncBuffer — лог сервера, который пишет другая горутина.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startServer запускает `ksb serve` в процессе на временной базе.
func startServer(t *testing.T) (string, *syncBuffer) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	t.Setenv("KSB_LISTEN", addr)
	t.Setenv("KSB_TRUSTED_PROXIES", "127.0.0.1")
	// Адрес сервера случайный, а публичный адрес OIDC должен с ним совпадать.
	if os.Getenv("KSB_OIDC_ISSUER") != "" {
		t.Setenv("KSB_PUBLIC_URL", "http://"+addr)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, nil, slog.New(slog.NewJSONHandler(logs, nil))) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	base := "http://" + addr
	for deadline := time.Now().Add(5 * time.Second); ; {
		res, err := http.Get(base + "/api/v1/health")
		if err == nil {
			res.Body.Close()
			return base, logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v\n%s", err, logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestReaderE2E проходит сценарии ограничения входов против настоящего serve:
// конфигурация, ограничитель и определение адреса клиента собраны как в проде.
func TestReaderE2E(t *testing.T) {
	t.Setenv("KSB_DATA_DIR", t.TempDir())
	var out, errb bytes.Buffer
	if code := run([]string{"user", "add", "alice"}, &out, &errb); code != 0 {
		t.Fatalf("user add: %s", errb.String())
	}
	if code := run([]string{"device", "add", "alice", "Kindle"}, &out, &errb); code != 0 {
		t.Fatalf("device add: %s", errb.String())
	}
	m := regexp.MustCompile(`(?m)^  password: ([a-z2-7]{4}-[a-z2-7]{4})$`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("device password not in xxxx-xxxx form: %s", out.String())
	}
	base, logs := startServer(t)

	const home = "198.51.100.7"
	const blocked = "Too many failed attempts. Try again in 15 minutes."
	at := func(ip, password string) reader {
		return reader{t: t, base: base, ip: ip, user: "alice", password: password}
	}
	kindle := at(home, m[1])
	doc := accounts.KeyFromPassword("e2e book")

	kindle.login().expect(t, "login", 200, "")
	kindle.push(doc, 0.25).expect(t, "push", 200, "")

	// Отозванная или забытая читалка в той же сети шлёт один и тот же старый
	// ключ: тратится одна неудача, адрес не блокируется.
	stale := at(home, "old-pass")
	for i := range 30 {
		stale.push(doc, 0.1).expect(t, fmt.Sprint("stale reader ", i), 401, "Unauthorized")
	}
	kindle.pull(doc).expect(t, "home not blocked by stale reader", 200, "")

	// Подбор с того же адреса: вместе с неудачей старого ключа — десятая.
	for i := range 9 {
		at(home, fmt.Sprint("guess", i)).login().expect(t, fmt.Sprint("guess ", i), 401, "Unauthorized")
	}
	a := at(home, "typo").login()
	a.expect(t, "wrong password from blocked address", 401, blocked)
	if a.retryAfter != "900" || a.body["code"] != float64(2001) {
		t.Fatalf("blocked answer: %+v", a)
	}

	// Читалка, уже прошедшая проверку, продолжает синхронизироваться.
	for i := range 5 {
		kindle.push(doc, 0.3+float64(i)/100).expect(t, fmt.Sprint("cached push ", i), 200, "")
	}
	if a := kindle.pull(doc); a.status != 200 || a.body["percentage"] != 0.34 {
		t.Fatalf("cached pull: %+v", a)
	}

	at("198.51.100.8", "guess-x").login().expect(t, "neighbour address", 401, "Unauthorized")

	log := logs.String()
	if n := strings.Count(log, `"msg":"auth: client blocked","ip":"198.51.100.7"`); n != 1 {
		t.Errorf("block warnings for %s: %d\n%s", home, n, log)
	}
	// В логе адрес клиента, а не прокси; повторы старого ключа и отказы из-за
	// блокировки не логируются: 1 старый ключ + 9 подборов + сосед.
	if n := strings.Count(log, "authorization failed"); n != 11 {
		t.Errorf("logged failures: %d\n%s", n, log)
	}
	if strings.Contains(log, `"ip":"127.0.0.1"`) {
		t.Errorf("proxy address logged instead of client:\n%s", log)
	}
}
