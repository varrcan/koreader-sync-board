package panelauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/netip"
	"strings"
	"sync"

	"github.com/varrcan/koreader-sync-board/server/internal/accounts"
	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
)

// ErrInvalidSetupCode — код настройки не совпал или его нет.
var ErrInvalidSetupCode = errors.New("invalid setup code")

// Setup — одноразовый код мастера первого запуска. Код живёт только в памяти
// процесса: перезапуск делает старый код недействительным. Таймаута нет: код из
// лога и есть защита, а окно по времени ничего к ней не добавляет.
type Setup struct {
	acc     *accounts.Service
	limiter *ratelimit.Limiter

	mu   sync.Mutex
	code string // нормализованный: без дефисов
}

// NewSetup создаёт Setup без кода. limiter может быть nil.
func NewSetup(acc *accounts.Service, limiter *ratelimit.Limiter) *Setup {
	return &Setup{acc: acc, limiter: limiter}
}

// Generate создаёт новый код вида xxxx-xxxx-xxxx (60 бит) и возвращает его для лога.
func (s *Setup) Generate() string {
	code := accounts.GeneratePassword(3)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = normalizeCode(code)
	return code
}

func (s *Setup) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Required — мастер нужен: код есть, а пользователей нет. Пользователь мог
// появиться из CLI, поэтому база проверяется каждый раз.
func (s *Setup) Required(ctx context.Context) (bool, error) {
	if s.current() == "" {
		return false, nil
	}
	n, err := s.acc.CountUsers(ctx)
	return n == 0, err
}

// Complete создаёт первого администратора по коду. Порядок: пользователи уже
// есть → accounts.ErrSetupCompleted без проверки кода; ограничитель; сравнение
// кода (неверный тратит бюджет); создание. Ошибка ввода (имя, длина пароля) при
// верном коде не тратит ни бюджет, ни код.
func (s *Setup) Complete(ctx context.Context, code, username, password string, client netip.Addr) (accounts.User, error) {
	n, err := s.acc.CountUsers(ctx)
	if err != nil {
		return accounts.User{}, err
	}
	if n > 0 {
		return accounts.User{}, accounts.ErrSetupCompleted
	}
	var att ratelimit.Attempt
	if s.limiter != nil {
		a, retry, ok := s.limiter.Acquire(client)
		if !ok {
			return accounts.User{}, &accounts.RateLimitedError{RetryAfter: retry}
		}
		att = a
	}
	want := s.current()
	got := normalizeCode(code)
	if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		att.Fail()
		return accounts.User{}, ErrInvalidSetupCode
	}
	att.Release()
	u, err := s.acc.CreateFirstAdmin(ctx, username, password)
	if err != nil {
		return accounts.User{}, err
	}
	s.mu.Lock()
	s.code = ""
	s.mu.Unlock()
	return u, nil
}

// normalizeCode приводит ввод к виду хранимого кода: без пробелов по краям, в
// нижнем регистре, без дефисов.
func normalizeCode(code string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(code)), "-", "")
}
