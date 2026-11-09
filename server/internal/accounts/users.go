package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrSelf — администратор применил действие экрана пользователей к себе:
	// для своей учётной записи есть проверки текущего пароля и способа входа.
	ErrSelf = errors.New("action on own account")
	// ErrForbidden — актор к моменту записи уже не действующий администратор.
	ErrForbidden = errors.New("actor is not an active administrator")
)

// adminTarget — окончание UPDATE и DELETE панели. Проверка актора входит в сам
// запрос, а записи идут через одно соединение, поэтому из двух встречных
// разжалований второе уже не пройдёт. Других условий здесь быть не должно:
// у нуля строк остаётся две причины, которые различает adminChange.
const adminTarget = ` WHERE id = ? AND EXISTS (
	SELECT 1 FROM users a WHERE a.id = ? AND a.role = 'admin' AND a.disabled_at IS NULL)
	RETURNING username`

// adminChange выполняет запрос, оканчивающийся adminTarget, и возвращает имя цели.
func adminChange(ctx context.Context, q Querier, actor, target int64, query string, args ...any) (string, error) {
	var username string
	err := q.QueryRowContext(ctx, query, append(args, target, actor)...).Scan(&username)
	if !errors.Is(err, sql.ErrNoRows) {
		return username, err
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, target).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrUserNotFound
	case err != nil:
		return "", err
	}
	return "", ErrForbidden
}

// UserInfo — пользователь для экрана администратора.
type UserInfo struct {
	User
	// DeviceCount — неотозванные устройства.
	DeviceCount int
	// LastActiveAt — последнее использование устройства, в том числе отозванного,
	// или сессии; nil, если их не было.
	LastActiveAt *time.Time
}

// Users возвращает пользователей по имени с устройствами и активностью.
func (s *Service) Users(ctx context.Context) ([]UserInfo, error) {
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT `+userColumns+`,
		       (SELECT count(*) FROM device_credentials d WHERE d.user_id = u.id AND d.revoked_at IS NULL),
		       (SELECT max(t) FROM (
		          SELECT d.last_used_at AS t FROM device_credentials d WHERE d.user_id = u.id
		          UNION ALL
		          SELECT s.last_used_at FROM sessions s WHERE s.user_id = u.id))
		FROM users u ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserInfo
	for rows.Next() {
		var info UserInfo
		var active sql.NullInt64
		info.User, err = scanUser(rows, &info.DeviceCount, &active)
		if err != nil {
			return nil, err
		}
		info.LastActiveAt = optTime(active)
		out = append(out, info)
	}
	return out, rows.Err()
}

// UserByID находит пользователя; не нашлось — ErrUserNotFound.
func (s *Service) UserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.db.Read.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return u, err
}

// CreateUserWithPassword создаёт пользователя со сгенерированным паролем панели.
// Актор и вставка — две команды одной транзакции, а не одно условие: ноль строк у
// ON CONFLICT DO NOTHING уже значит занятое имя.
func (s *Service) CreateUserWithPassword(ctx context.Context, actor int64, username string, role Role) (User, string, error) {
	if !ValidUsername(username) {
		return User{}, "", ErrInvalidUsername
	}
	if err := checkRole(role); err != nil {
		return User{}, "", err
	}
	password := GeneratePassword(4)
	hash, err := s.hash(password)
	if err != nil {
		return User{}, "", err
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE id = ? AND role = 'admin' AND disabled_at IS NULL`, actor).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrForbidden
	}
	if err != nil {
		return User{}, "", err
	}
	now := s.now().Unix()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO users (username, role, panel_password_hash, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (username) DO NOTHING`,
		username, role, hash, now)
	if err != nil {
		return User{}, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, "", ErrUserExists
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return User{}, "", err
	}
	s.forgetRejected(username)
	return User{ID: id, Username: username, Role: role, CreatedAt: time.Unix(now, 0), HasPassword: true}, password, nil
}

// ResetPanelPassword выдаёт другому пользователю новый пароль панели и в той же
// транзакции закрывает все его сессии. Возвращает имя цели и пароль.
func (s *Service) ResetPanelPassword(ctx context.Context, actor, target int64) (string, string, error) {
	if actor == target {
		return "", "", ErrSelf
	}
	password := GeneratePassword(4)
	hash, err := s.hash(password)
	if err != nil {
		return "", "", err
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	username, err := adminChange(ctx, tx, actor, target, `UPDATE users SET panel_password_hash = ?`+adminTarget, hash)
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, target); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return username, password, nil
}

// UserUpdate — изменяемые поля; nil — оставить как есть.
type UserUpdate struct {
	Role     *Role
	Disabled *bool
}

// UpdateUser меняет роль и статус другого пользователя и возвращает его имя.
// Прежний статус читается в той же транзакции (записи на ней не вклиниваются),
// поэтому WHERE обходится без условий на состояние цели, а включение уже
// включённого остаётся успехом и не трогает его сессии.
func (s *Service) UpdateUser(ctx context.Context, actor, target int64, upd UserUpdate) (string, error) {
	if actor == target {
		return "", ErrSelf
	}
	if upd.Role == nil && upd.Disabled == nil {
		return "", fmt.Errorf("%w: nothing to update", ErrInvalid)
	}
	if upd.Role != nil {
		if err := checkRole(*upd.Role); err != nil {
			return "", err
		}
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var disabled sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT disabled_at FROM users WHERE id = ?`, target).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", err
	}
	enabling := upd.Disabled != nil && !*upd.Disabled && disabled.Valid
	switch {
	case enabling:
		disabled = sql.NullInt64{}
	case upd.Disabled != nil && *upd.Disabled && !disabled.Valid:
		disabled = sql.NullInt64{Int64: s.now().Unix(), Valid: true}
	}
	username, err := adminChange(ctx, tx, actor, target,
		`UPDATE users SET role = coalesce(?, role), disabled_at = ?`+adminTarget, upd.Role, disabled)
	if err != nil {
		return "", err
	}
	if enabling {
		if err := s.finishEnable(ctx, tx, target, username); err != nil {
			return "", err
		}
		return username, nil
	}
	return username, tx.Commit()
}

// EnableUser снимает отключение (CLI). Пользователь, который не был отключён, не
// меняется: его сессии действуют. Отказ, запомненный работающим сервером, из
// другого процесса не забыть, он истечёт сам.
func (s *Service) EnableUser(ctx context.Context, username string) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var disabled sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id, disabled_at FROM users WHERE username = ?`, username).Scan(&id, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !disabled.Valid {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = NULL WHERE id = ?`, id); err != nil {
		return err
	}
	return s.finishEnable(ctx, tx, id, username)
}

// finishEnable завершает транзакцию включения. Отключение сессии не удаляет,
// поэтому без этого шага ожили бы входы, открытые до блокировки. Ключ читалки не
// менялся и остался среди запомненных отказов, их тоже нужно забыть.
func (s *Service) finishEnable(ctx context.Context, tx *sql.Tx, id int64, username string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.forgetRejected(username)
	return nil
}

// AdminDeleteUser удаляет другого пользователя с теми же данными, что DeleteUser,
// и возвращает его имя.
func (s *Service) AdminDeleteUser(ctx context.Context, actor, target int64) (string, error) {
	if actor == target {
		return "", ErrSelf
	}
	return adminChange(ctx, s.db.Write, actor, target, `DELETE FROM users`+adminTarget)
}

// AdminUnlinkIdentity снимает привязку OIDC другого пользователя и возвращает его
// имя; непривязанный пользователь — не ошибка. Пароль не проверяется: его можно выдать сбросом.
func (s *Service) AdminUnlinkIdentity(ctx context.Context, actor, target int64) (string, error) {
	if actor == target {
		return "", ErrSelf
	}
	return adminChange(ctx, s.db.Write, actor, target, `UPDATE users SET oidc_issuer = NULL, oidc_subject = NULL`+adminTarget)
}
