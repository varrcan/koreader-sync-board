package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UserByIdentity находит пользователя, привязанного к паре (issuer, subject),
// в том числе отключённого; не нашлось — ErrNotFound.
func (s *Service) UserByIdentity(ctx context.Context, issuer, subject string) (User, error) {
	u, err := scanUser(s.db.Read.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users u WHERE u.oidc_issuer = ? AND u.oidc_subject = ?`, issuer, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// CreateOIDCUser создаёт пользователя без пароля панели и устройств, привязанного
// к паре (issuer, subject). Проверка привязки и вставка — один запрос, поэтому из
// одновременных вызовов с одним subject выигрывает один, остальные получают
// ErrIdentityTaken. Занятое имя — ErrUserExists.
func (s *Service) CreateOIDCUser(ctx context.Context, username string, role Role, issuer, subject string) (User, error) {
	if !ValidUsername(username) {
		return User{}, ErrInvalidUsername
	}
	if err := checkRole(role); err != nil {
		return User{}, err
	}
	now := s.now().Unix()
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO users (username, role, created_at, oidc_issuer, oidc_subject)
		SELECT ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE oidc_issuer = ? AND oidc_subject = ?)
		ON CONFLICT (username) DO NOTHING`,
		username, role, now, issuer, subject, issuer, subject)
	if err != nil {
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.UserByIdentity(ctx, issuer, subject); err == nil {
			return User{}, ErrIdentityTaken
		}
		return User{}, ErrUserExists
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: role, CreatedAt: time.Unix(now, 0), OIDCLinked: true}, nil
}

// LinkIdentity привязывает пару (issuer, subject) к пользователю, заменяя прежнюю
// привязку. Пара у другого пользователя — ErrIdentityTaken, пользователя нет — ErrNotFound.
func (s *Service) LinkIdentity(ctx context.Context, userID int64, issuer, subject string) error {
	res, err := s.db.Write.ExecContext(ctx, `
		UPDATE users SET oidc_issuer = ?, oidc_subject = ?
		WHERE id = ? AND NOT EXISTS (
			SELECT 1 FROM users WHERE oidc_issuer = ? AND oidc_subject = ? AND id != ?)`,
		issuer, subject, userID, issuer, subject, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var one int
	err = s.db.Read.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	return ErrIdentityTaken
}

// UnlinkIdentity удаляет привязку OIDC; непривязанный пользователь — не ошибка.
// Есть ли у пользователя другой способ входа, проверяет вызывающий.
func (s *Service) UnlinkIdentity(ctx context.Context, username string) error {
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE users SET oidc_issuer = NULL, oidc_subject = NULL WHERE username = ?`, username)
	return affected(res, err)
}

// SetRole меняет роль пользователя.
func (s *Service) SetRole(ctx context.Context, username string, role Role) error {
	if err := checkRole(role); err != nil {
		return err
	}
	res, err := s.db.Write.ExecContext(ctx, `UPDATE users SET role = ? WHERE username = ?`, role, username)
	return affected(res, err)
}
