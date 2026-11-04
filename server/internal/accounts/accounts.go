// Package accounts управляет пользователями и учётными данными устройств и
// проверяет ключи kosync.
//
// Ключ kosync — это md5 пароля устройства (32 hex-символа), который KOReader шлёт
// в x-auth-key. В базе хранится только argon2id от ключа. Удачные проверки
// кешируются, чтобы не платить argon2 на каждый запрос, но актуальность (отзыв,
// отключение, смена ключа) сверяется с базой всегда: устройствами управляет и CLI,
// отдельный процесс, кеш сервера которому недоступен.
//
// С ограничителем (WithLimiter) неудачные проверки считаются по адресу клиента,
// а неудачные пары «пользователь — ключ» запоминаются: повтор того же неверного
// ключа (отозванная читалка) отвечается без argon2 и бюджет адреса не тратит.
package accounts

import (
	"cmp"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
	"github.com/varrcan/koreader-sync-board/server/internal/store"
)

var (
	// ErrUnauthorized — ключ не подошёл, устройство отозвано или пользователь отключён.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrUnknownUser — пользователя нет. Оборачивает ErrUnauthorized.
	ErrUnknownUser  = fmt.Errorf("%w: unknown user", ErrUnauthorized)
	ErrUserExists   = errors.New("user already exists")
	ErrDeviceExists = errors.New("device with this name already exists")
	ErrNotFound     = errors.New("not found")
	ErrInvalid      = errors.New("invalid argument")
	// ErrRateLimited — адрес клиента заблокирован за неудачные входы. Возвращается
	// как *RateLimitedError и ErrUnauthorized не оборачивает.
	ErrRateLimited = errors.New("too many failed attempts")
	// ErrInvalidUsername и ErrInvalidPassword оборачивают ErrInvalid.
	ErrInvalidUsername = fmt.Errorf("%w: username must be non-empty and must not contain ':'", ErrInvalid)
	ErrInvalidPassword = fmt.Errorf("%w: password must be %d to %d characters", ErrInvalid, MinPanelPassword, MaxPanelPassword)
	// ErrSetupCompleted — первый администратор уже есть.
	ErrSetupCompleted = errors.New("setup already completed")
	// ErrRepeated сопровождает ErrUnauthorized или ErrUnknownUser, когда тот же
	// ключ уже был отвергнут недавно. Такие отказы не стоит логировать: их шлёт
	// отозванная читалка, и fail2ban по логу забанил бы её домашнюю сеть.
	ErrRepeated = errors.New("key already rejected")
)

// RateLimitedError — ErrRateLimited со сроком, через который стоит повторить.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%v: retry after %v", ErrRateLimited, e.RetryAfter)
}

func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// Role — роль пользователя.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

const (
	// MinPanelPassword и MaxPanelPassword — допустимая длина пароля панели в символах.
	MinPanelPassword = 8
	MaxPanelPassword = 256
)

const (
	cacheTTL         = 10 * time.Minute
	cacheMaxEntries  = 10_000
	lastUsedInterval = time.Minute
)

// User — пользователь без секретов.
type User struct {
	ID         int64
	Username   string
	Role       Role
	CreatedAt  time.Time
	DisabledAt *time.Time
}

// Device — учётные данные устройства без ключа.
type Device struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Principal — результат успешной проверки ключа.
type Principal struct {
	UserID       int64
	Username     string
	CredentialID int64
	KeyRev       int64
}

type cacheEntry struct {
	credentialID int64
	keyRev       int64
	expires      time.Time
}

func (e cacheEntry) expiresAt() time.Time { return e.expires }

// rejectEntry — запомненная неудача; user — хеш имени, чтобы забыть неудачи
// пользователя при смене его учётных данных. Хеш, а не строка: имя приходит
// из заголовка (до 1 MiB), и 10 000 записей с длинными именами съели бы гигабайты.
type rejectEntry struct {
	user    [32]byte
	expires time.Time
}

func (e rejectEntry) expiresAt() time.Time { return e.expires }

// Service — пользователи и устройства поверх одной базы.
type Service struct {
	db     *store.DB
	now    func() time.Time
	params Argon2Params

	// sem ограничивает число одновременных argon2: каждое берёт ~19 MiB.
	sem   chan struct{}
	dummy string // хеш случайного ключа для фиктивной проверки

	limiter *ratelimit.Limiter

	mu       sync.Mutex
	cache    map[[32]byte]cacheEntry
	rejected map[[32]byte]rejectEntry

	argonCalls     atomic.Int64 // для тестов
	lastUsedWrites atomic.Int64 // для тестов
}

// Option настраивает Service.
type Option func(*Service)

// WithClock подменяет источник времени.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithArgon2Params задаёт параметры argon2id для новых хешей.
func WithArgon2Params(p Argon2Params) Option { return func(s *Service) { s.params = p } }

// WithLimiter включает ограничение неудачных проверок по адресу клиента и
// запоминание неудачных пар «пользователь — ключ».
func WithLimiter(l *ratelimit.Limiter) Option { return func(s *Service) { s.limiter = l } }

// New создаёт сервис.
func New(db *store.DB, opts ...Option) *Service {
	s := &Service{
		db:       db,
		now:      time.Now,
		params:   DefaultArgon2Params,
		sem:      make(chan struct{}, runtime.GOMAXPROCS(0)),
		cache:    map[[32]byte]cacheEntry{},
		rejected: map[[32]byte]rejectEntry{},
	}
	for _, o := range opts {
		o(s)
	}
	dummy, err := hashSecret(rand.Text(), s.params)
	if err != nil {
		panic(err) // crypto/rand не падает
	}
	s.dummy = dummy
	return s
}

// ValidUsername — непустое имя без «:», как is_valid_key_field эталона.
func ValidUsername(name string) bool {
	return name != "" && !strings.Contains(name, ":")
}

// KeyFromPassword возвращает ключ kosync для пароля устройства: md5 в hex.
func KeyFromPassword(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

func (s *Service) hash(secret string) (string, error) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	s.argonCalls.Add(1)
	return hashSecret(secret, s.params)
}

func (s *Service) verify(secret, encoded string) bool {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	s.argonCalls.Add(1)
	ok, err := verifySecret(secret, encoded)
	return err == nil && ok
}

// CreateUser создаёт пользователя без устройств.
func (s *Service) CreateUser(ctx context.Context, username string, role Role) (User, error) {
	if !ValidUsername(username) {
		return User{}, ErrInvalidUsername
	}
	if role != RoleAdmin && role != RoleUser {
		return User{}, fmt.Errorf("%w: role must be admin or user", ErrInvalid)
	}
	now := s.now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO users (username, role, created_at) VALUES (?, ?, ?) ON CONFLICT (username) DO NOTHING`,
		username, role, now)
	if err != nil {
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrUserExists
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: role, CreatedAt: time.Unix(now, 0)}, nil
}

// CreateUserWithDevice создаёт пользователя с ролью user и одним устройством с
// готовым ключом (POST /users/create: пароль в теле — это уже md5). С ограничителем
// каждая попытка, удачная тоже, расходует бюджет адреса client: ограничивается
// именно массовое создание аккаунтов.
func (s *Service) CreateUserWithDevice(ctx context.Context, username, deviceName, key string, client netip.Addr) error {
	if s.limiter != nil {
		att, retry, ok := s.limiter.Acquire(client)
		if !ok {
			return &RateLimitedError{RetryAfter: retry}
		}
		defer att.Fail()
	}
	keyHash, err := s.hash(key)
	if err != nil {
		return err
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (username, role, created_at) VALUES (?, 'user', ?) ON CONFLICT (username) DO NOTHING`,
		username, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserExists
	}
	userID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO device_credentials (user_id, name, key_hash, created_at) VALUES (?, ?, ?, ?)`,
		userID, deviceName, keyHash, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.forgetRejected(username)
	return nil
}

// ListUsers возвращает пользователей по имени.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, username, role, created_at, disabled_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created int64
		var disabled sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &created, &disabled); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0)
		u.DisabledAt = optTime(disabled)
		out = append(out, u)
	}
	return out, rows.Err()
}

// DisableUser отключает пользователя: ни одно его устройство больше не проходит проверку.
func (s *Service) DisableUser(ctx context.Context, username string) error {
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE users SET disabled_at = coalesce(disabled_at, ?) WHERE username = ?`, s.now().Unix(), username)
	return affected(res, err)
}

// DeleteUser удаляет пользователя с устройствами, журналом и текущим прогрессом.
// Общий реестр документов не меняется.
func (s *Service) DeleteUser(ctx context.Context, username string) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	return affected(res, err)
}

// DeleteUserByID — то же по идентификатору (DELETE /users/me).
func (s *Service) DeleteUserByID(ctx context.Context, id int64) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return affected(res, err)
}

// AddDevice создаёт устройство и возвращает его пароль. Пароль больше нигде не
// хранится и не может быть показан повторно.
func (s *Service) AddDevice(ctx context.Context, username, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%w: device name is empty", ErrInvalid)
	}
	password := GeneratePassword(2)
	keyHash, err := s.hash(KeyFromPassword(password))
	if err != nil {
		return "", err
	}
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO device_credentials (user_id, name, key_hash, created_at)
		SELECT id, ?, ?, ? FROM users WHERE username = ?
		ON CONFLICT DO NOTHING`,
		name, keyHash, s.now().Unix(), username)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := s.db.Write.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return "", fmt.Errorf("user %q: %w", username, ErrNotFound)
		}
		return "", ErrDeviceExists
	}
	s.forgetRejected(username)
	return password, nil
}

// GeneratePassword возвращает groups групп по 4 символа base32 в нижнем регистре
// через дефис, по 20 бит на группу (не больше 6 групп). Пароль устройства — две
// группы: его набирают на экранной клавиатуре читалки, а онлайн-подбор сдерживает
// ограничитель. В алфавите нет 0/1/8/9, путаницы с o/l нет.
func GeneratePassword(groups int) string {
	t := strings.ToLower(rand.Text())
	parts := make([]string, groups)
	for i := range parts {
		parts[i] = t[4*i : 4*i+4]
	}
	return strings.Join(parts, "-")
}

// ListDevices возвращает устройства пользователя, включая отозванные.
func (s *Service) ListDevices(ctx context.Context, username string) ([]Device, error) {
	var userID int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT id, name, created_at, last_used_at, revoked_at
		FROM device_credentials WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var created int64
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Name, &created, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		d.CreatedAt = time.Unix(created, 0)
		d.LastUsedAt = optTime(lastUsed)
		d.RevokedAt = optTime(revoked)
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice отзывает действующее устройство пользователя по имени.
func (s *Service) RevokeDevice(ctx context.Context, username, name string) error {
	res, err := s.db.Write.ExecContext(ctx, `
		UPDATE device_credentials SET revoked_at = ?
		WHERE revoked_at IS NULL AND name = ?
		  AND user_id = (SELECT id FROM users WHERE username = ?)`,
		s.now().Unix(), name, username)
	return affected(res, err)
}

// Authenticate проверяет ключ kosync, пришедший с адреса client. Для
// несуществующего пользователя и пользователя без устройств выполняется
// фиктивная проверка, чтобы время ответа не выдавало, существует ли имя.
//
// Порядок: кеш удачных проверок (со сверкой в БД) → запомненные неудачи →
// ограничитель → argon2. Закешированное устройство проходит и с заблокированного
// адреса; остальные с него получают *RateLimitedError без argon2.
func (s *Service) Authenticate(ctx context.Context, username, key string, client netip.Addr) (Principal, error) {
	var userID int64
	var disabled sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT id, disabled_at FROM users WHERE username = ?`, username).Scan(&userID, &disabled)
	// reject — ошибка, если ключ не подойдёт заведомо; проверка всё равно идёт
	// по общему пути, чтобы ни время, ни бюджет не выдавали имя.
	var reject error
	switch {
	case errors.Is(err, sql.ErrNoRows):
		reject = ErrUnknownUser
	case err != nil:
		return Principal{}, err
	case disabled.Valid:
		reject = ErrUnauthorized
	}

	ck := cacheKey(userID, key)
	if reject == nil {
		if p, ok, err := s.fromCache(ctx, ck, userID, username); ok || err != nil {
			return p, err
		}
	}

	rk := rejectKey(username, key)
	if s.limiter != nil && s.wasRejected(rk) {
		return Principal{}, fmt.Errorf("%w: %w", cmp.Or(reject, ErrUnauthorized), ErrRepeated)
	}
	var att ratelimit.Attempt
	if s.limiter != nil {
		a, retry, ok := s.limiter.Acquire(client)
		if !ok {
			return Principal{}, &RateLimitedError{RetryAfter: retry}
		}
		att = a
	}

	p, err := s.verifyCredentials(ctx, ck, userID, username, key, reject)
	switch {
	case errors.Is(err, ErrUnauthorized):
		att.Fail()
		if s.limiter != nil {
			s.rememberRejected(rk, username)
		}
	default:
		att.Release()
	}
	return p, err
}

// fromCache проверяет кеш удачных проверок и сверяет запись с БД. Попадание
// продлевает срок записи: активная читалка не выпадает из кеша, пока её адрес
// заблокирован. Это безопасно, потому что каждое попадание сверяется с БД.
func (s *Service) fromCache(ctx context.Context, ck [32]byte, userID int64, username string) (Principal, bool, error) {
	e, ok := s.cached(ck)
	if !ok {
		return Principal{}, false, nil
	}
	var lastUsed sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx, `
		SELECT d.last_used_at FROM device_credentials d JOIN users u ON u.id = d.user_id
		WHERE d.id = ? AND d.user_id = ? AND d.key_rev = ?
		  AND d.revoked_at IS NULL AND u.disabled_at IS NULL`,
		e.credentialID, userID, e.keyRev).Scan(&lastUsed)
	switch {
	case err == nil:
		e.expires = s.now().Add(cacheTTL)
		s.store(ck, e)
		p := Principal{UserID: userID, Username: username, CredentialID: e.credentialID, KeyRev: e.keyRev}
		return p, true, s.touch(ctx, p.CredentialID, lastUsed)
	case errors.Is(err, sql.ErrNoRows):
		s.evict(ck)
		return Principal{}, false, nil
	default:
		return Principal{}, false, err
	}
}

// verifyCredentials — медленная проверка argon2 по активным устройствам или
// фиктивная, если reject != nil или устройств нет.
func (s *Service) verifyCredentials(ctx context.Context, ck [32]byte, userID int64, username, key string, reject error) (Principal, error) {
	if reject != nil {
		s.verify(key, s.dummy)
		return Principal{}, reject
	}
	creds, err := s.activeCredentials(ctx, userID)
	if err != nil {
		return Principal{}, err
	}
	if len(creds) == 0 {
		s.verify(key, s.dummy)
		return Principal{}, ErrUnauthorized
	}
	for _, c := range creds {
		if s.verify(key, c.hash) {
			s.store(ck, cacheEntry{credentialID: c.id, keyRev: c.rev, expires: s.now().Add(cacheTTL)})
			p := Principal{UserID: userID, Username: username, CredentialID: c.id, KeyRev: c.rev}
			return p, s.touch(ctx, c.id, c.lastUsed)
		}
	}
	return Principal{}, ErrUnauthorized
}

type credential struct {
	id, rev  int64
	hash     string
	lastUsed sql.NullInt64
}

// activeCredentials возвращает неотозванные устройства, недавно использованные — первыми.
func (s *Service) activeCredentials(ctx context.Context, userID int64) ([]credential, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT id, key_rev, key_hash, last_used_at FROM device_credentials
		WHERE user_id = ? AND revoked_at IS NULL
		ORDER BY last_used_at DESC NULLS LAST, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var creds []credential
	for rows.Next() {
		var c credential
		if err := rows.Scan(&c.id, &c.rev, &c.hash, &c.lastUsed); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

// touch записывает last_used_at, если сохранённое значение старше минуты.
func (s *Service) touch(ctx context.Context, credentialID int64, lastUsed sql.NullInt64) error {
	now := s.now().Unix()
	if lastUsed.Valid && now-lastUsed.Int64 < int64(lastUsedInterval/time.Second) {
		return nil
	}
	s.lastUsedWrites.Add(1)
	_, err := s.db.Write.ExecContext(ctx,
		`UPDATE device_credentials SET last_used_at = ? WHERE id = ?`, now, credentialID)
	return err
}

// Querier — общее у *sql.DB и *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CheckTx повторно проверяет принципала внутри транзакции записи: устройство не
// отозвано, пользователь не отключён, ключ не менялся с момента проверки.
func CheckTx(ctx context.Context, q Querier, p Principal) error {
	var one int
	err := q.QueryRowContext(ctx, `
		SELECT 1 FROM device_credentials d JOIN users u ON u.id = d.user_id
		WHERE d.id = ? AND d.user_id = ? AND d.key_rev = ?
		  AND d.revoked_at IS NULL AND u.disabled_at IS NULL`,
		p.CredentialID, p.UserID, p.KeyRev).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	return err
}

// RotateKey меняет ключ устройства, которым выполнен вход. key_rev растёт,
// поэтому закешированные проверки старого ключа перестают действовать сразу.
func (s *Service) RotateKey(ctx context.Context, p Principal, newKey string) error {
	keyHash, err := s.hash(newKey)
	if err != nil {
		return err
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := CheckTx(ctx, tx, p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE device_credentials SET key_hash = ?, key_rev = key_rev + 1 WHERE id = ?`,
		keyHash, p.CredentialID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.forgetRejected(p.Username)
	return nil
}

// Bootstrap создаёт администратора из ADMIN_USER/ADMIN_PASSWORD, если пользователей
// ещё нет. Пустые оба значения — ничего не делать; задать только одно запрещает config.
// Устройств у администратора нет: пароль панели в kosync не работает.
func (s *Service) Bootstrap(ctx context.Context, username, password string) (bool, error) {
	if username == "" && password == "" {
		return false, nil
	}
	if n, err := s.CountUsers(ctx); err != nil || n > 0 {
		return false, err
	}
	_, err := s.CreateFirstAdmin(ctx, username, password)
	switch {
	case errors.Is(err, ErrSetupCompleted):
		return false, nil
	case errors.Is(err, ErrInvalidUsername):
		return false, fmt.Errorf("ADMIN_USER: %w", err)
	case errors.Is(err, ErrInvalidPassword):
		return false, fmt.Errorf("ADMIN_PASSWORD: %w", err)
	}
	return err == nil, err
}

func cacheKey(userID int64, key string) [32]byte {
	h := sha256.New()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(userID))
	h.Write(b[:])
	h.Write([]byte(key))
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// rejectKey — ключ запомненной неудачи. По имени, а не по id: неудачи
// несуществующих пользователей тоже запоминаются. Длина имени входит в хеш,
// чтобы пары ("ab", "c…") и ("a", "bc…") не совпадали.
func rejectKey(username, key string) [32]byte {
	h := sha256.New()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(username)))
	h.Write(b[:])
	h.Write([]byte(username))
	h.Write([]byte(key))
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func (s *Service) cached(k [32]byte) (cacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lookupTTL(s.cache, k, s.now())
}

func (s *Service) store(k [32]byte, e cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	putBounded(s.cache, k, e, s.now())
}

func (s *Service) evict(k [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, k)
}

func (s *Service) wasRejected(k [32]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := lookupTTL(s.rejected, k, s.now())
	return ok
}

func (s *Service) rememberRejected(k [32]byte, username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	putBounded(s.rejected, k, rejectEntry{user: sha256.Sum256([]byte(username)), expires: s.now().Add(cacheTTL)}, s.now())
}

// forgetRejected забывает неудачи пользователя, чьи учётные данные изменились:
// ранее отвергнутый ключ мог стать верным. Изменения из CLI (другой процесс)
// сюда не доходят, но CLI выдаёт случайные пароли, а остальное ограничивает TTL.
func (s *Service) forgetRejected(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := sha256.Sum256([]byte(username))
	for k, e := range s.rejected {
		if e.user == user {
			delete(s.rejected, k)
		}
	}
}

type expiring interface{ expiresAt() time.Time }

func lookupTTL[V expiring](m map[[32]byte]V, k [32]byte, now time.Time) (V, bool) {
	e, ok := m[k]
	if ok && now.After(e.expiresAt()) {
		delete(m, k)
		var zero V
		return zero, false
	}
	return e, ok
}

// putBounded не даёт кешу расти бесконечно: при переполнении выбрасываются
// просроченные записи, а если их нет — все.
func putBounded[V expiring](m map[[32]byte]V, k [32]byte, v V, now time.Time) {
	if _, ok := m[k]; !ok && len(m) >= cacheMaxEntries {
		for k, e := range m {
			if now.After(e.expiresAt()) {
				delete(m, k)
			}
		}
		if len(m) >= cacheMaxEntries {
			clear(m)
		}
	}
	m[k] = v
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}
