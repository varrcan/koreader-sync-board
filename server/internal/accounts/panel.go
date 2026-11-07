package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/varrcan/koreader-sync-board/server/internal/ratelimit"
)

// SessionTTL — срок сессии панели без использования; каждое использование продлевает его.
const SessionTTL = 30 * 24 * time.Hour

// Session — сессия панели без токена.
type Session struct {
	ID        int64
	UserID    int64
	ExpiresAt time.Time
	// Renewed — срок только что продлён: клиенту стоит выставить cookie заново.
	Renewed bool
}

// ValidPanelPassword проверяет длину пароля панели.
func ValidPanelPassword(password string) bool {
	n := utf8.RuneCountInString(password)
	return n >= MinPanelPassword && n <= MaxPanelPassword
}

// CountUsers возвращает число пользователей, включая отключённых.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// VerifyPanelPassword проверяет пароль панели, пришедший с адреса client. Как и в
// Authenticate, неизвестное имя, отключённый пользователь и пользователь без
// пароля панели проверяются на фиктивном хеше, а бюджет ограничителя тратится
// одинаково. Кеша удачных проверок и памяти отвергнутых пар нет: человек вводит
// пароль редко, а повтор одного неверного пароля — это и есть подбор.
func (s *Service) VerifyPanelPassword(ctx context.Context, username, password string, client netip.Addr) (User, error) {
	var hash sql.NullString
	u, err := scanUser(s.db.Read.QueryRowContext(ctx,
		`SELECT `+userColumns+`, u.panel_password_hash FROM users u WHERE u.username = ?`, username), &hash)
	var reject error
	switch {
	case errors.Is(err, sql.ErrNoRows):
		reject = ErrUnknownUser
	case err != nil:
		return User{}, err
	case u.DisabledAt != nil, !hash.Valid:
		reject = ErrUnauthorized
	}

	var att ratelimit.Attempt
	if s.limiter != nil {
		a, retry, ok := s.limiter.Acquire(client)
		if !ok {
			return User{}, &RateLimitedError{RetryAfter: retry}
		}
		att = a
	}
	if reject != nil {
		s.verify(password, s.dummy)
	} else if !s.verify(password, hash.String) {
		reject = ErrUnauthorized
	}
	if reject != nil {
		att.Fail()
		return User{}, reject
	}
	att.Release()
	return u, nil
}

// SetPanelPassword меняет пароль панели и в той же транзакции удаляет сессии
// пользователя, кроме keepSession (0 — удалить все).
func (s *Service) SetPanelPassword(ctx context.Context, username, password string, keepSession int64) error {
	if !ValidPanelPassword(password) {
		return ErrInvalidPassword
	}
	hash, err := s.hash(password)
	if err != nil {
		return err
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	err = tx.QueryRowContext(ctx,
		`UPDATE users SET panel_password_hash = ? WHERE username = ? RETURNING id`, hash, username).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, keepSession); err != nil {
		return err
	}
	return tx.Commit()
}

// SetFirstPanelPassword задаёт пароль панели пользователю, у которого его нет
// (создан OIDC-входом), не трогая сессии: личность уже подтверждена сессией.
// Пароль уже задан — ErrUnauthorized, менять его нужно через проверку текущего.
func (s *Service) SetFirstPanelPassword(ctx context.Context, userID int64, password string) error {
	if !ValidPanelPassword(password) {
		return ErrInvalidPassword
	}
	hash, err := s.hash(password)
	if err != nil {
		return err
	}
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE users SET panel_password_hash = ? WHERE id = ? AND panel_password_hash IS NULL`, hash, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUnauthorized
	}
	return nil
}

// CreateFirstAdmin создаёт администратора с паролем панели, только если
// пользователей нет. Проверка и вставка — один запрос, поэтому из двух
// одновременных вызовов (мастер и CLI, два мастера) выигрывает один, а второй
// получает ErrSetupCompleted.
func (s *Service) CreateFirstAdmin(ctx context.Context, username, password string) (User, error) {
	if !ValidUsername(username) {
		return User{}, ErrInvalidUsername
	}
	if !ValidPanelPassword(password) {
		return User{}, ErrInvalidPassword
	}
	hash, err := s.hash(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().Unix()
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO users (username, role, panel_password_hash, created_at)
		SELECT ?, 'admin', ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		username, hash, now)
	if err != nil {
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrSetupCompleted
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: RoleAdmin, CreatedAt: time.Unix(now, 0), HasPassword: true}, nil
}

// CreateSession открывает сессию и возвращает её токен. Токен в базе не
// хранится, только sha256 от него. Заодно удаляются просроченные сессии всех
// пользователей: фоновой чистки нет, а строк единицы.
func (s *Service) CreateSession(ctx context.Context, userID int64) (string, Session, error) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	now := s.now()
	expires := now.Add(SessionTTL)

	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return "", Session{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return "", Session{}, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (user_id, token_hash, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		userID, hash[:], now.Unix(), now.Unix(), expires.Unix())
	if err != nil {
		return "", Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", Session{}, err
	}
	id, _ := res.LastInsertId()
	return token, Session{ID: id, UserID: userID, ExpiresAt: time.Unix(expires.Unix(), 0)}, nil
}

// SessionByToken находит действующую сессию и её владельца. Сверка идёт с базой
// на каждый запрос, поэтому отключение и удаление пользователя (в том числе из
// CLI) и отзыв сессии действуют сразу. Срок продлевается не чаще раза в минуту.
func (s *Service) SessionByToken(ctx context.Context, token string) (Session, User, error) {
	if token == "" {
		return Session{}, User{}, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	now := s.now().Unix()
	var sess Session
	var lastUsed, expires int64
	u, err := scanUser(s.db.Read.QueryRowContext(ctx, `
		SELECT `+userColumns+`, s.id, s.last_used_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`,
		hash[:], now), &sess.ID, &lastUsed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, User{}, err
	}
	sess.UserID = u.ID
	if now-lastUsed >= int64(lastUsedInterval/time.Second) {
		expires = now + int64(SessionTTL/time.Second)
		if _, err := s.db.Write.ExecContext(ctx,
			`UPDATE sessions SET last_used_at = ?, expires_at = ? WHERE id = ?`, now, expires, sess.ID); err != nil {
			return Session{}, User{}, err
		}
		sess.Renewed = true
	}
	sess.ExpiresAt = time.Unix(expires, 0)
	return sess, u, nil
}

// DeleteSession удаляет сессию; несуществующая — не ошибка.
func (s *Service) DeleteSession(ctx context.Context, id int64) error {
	_, err := s.db.Write.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}
