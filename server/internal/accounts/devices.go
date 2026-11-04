package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxDeviceName — наибольшая длина имени устройства в символах.
const MaxDeviceName = 64

// ErrInvalidDeviceName оборачивает ErrInvalid.
var ErrInvalidDeviceName = fmt.Errorf("%w: device name must be 1 to %d characters", ErrInvalid, MaxDeviceName)

// ErrUserNotFound — владельца нового устройства нет. Оборачивает ErrNotFound;
// панель отличает его от несуществующего устройства.
var ErrUserNotFound = fmt.Errorf("user %w", ErrNotFound)

// Scope — чьи устройства доступны: свои (UserID) или все (All, администратор).
// Устройство вне области не отличается от несуществующего.
type Scope struct {
	UserID int64
	All    bool
}

// DeviceInfo — устройство для панели: с владельцем и тем, что сообщила о себе
// читалка в последнем событии прогресса (nil, если событий нет).
type DeviceInfo struct {
	Device
	Owner          string
	ReportedDevice *string
}

// normalizeDeviceName обрезает пробелы по краям и проверяет длину.
func normalizeDeviceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxDeviceName {
		return "", ErrInvalidDeviceName
	}
	return name, nil
}

// AddDevice создаёт устройство пользователю по имени (CLI, администратор панели)
// и возвращает его пароль. Пароль больше нигде не хранится и не может быть
// показан повторно.
func (s *Service) AddDevice(ctx context.Context, username, name string) (DeviceInfo, string, error) {
	d, pass, err := s.addDevice(ctx, `username = ?`, username, name)
	if errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("%w %q", err, username)
	}
	return d, pass, err
}

// AddDeviceFor — то же по идентификатору пользователя (владелец сессии панели).
func (s *Service) AddDeviceFor(ctx context.Context, userID int64, name string) (DeviceInfo, string, error) {
	return s.addDevice(ctx, `id = ?`, userID, name)
}

// addDevice — общий путь вставки; userCond выбирает пользователя по user.
func (s *Service) addDevice(ctx context.Context, userCond string, user any, name string) (DeviceInfo, string, error) {
	name, err := normalizeDeviceName(name)
	if err != nil {
		return DeviceInfo{}, "", err
	}
	password := GeneratePassword(2)
	keyHash, err := s.hash(KeyFromPassword(password))
	if err != nil {
		return DeviceInfo{}, "", err
	}
	res, err := s.db.Write.ExecContext(ctx, `
		INSERT INTO device_credentials (user_id, name, key_hash, created_at)
		SELECT id, ?, ?, ? FROM users WHERE `+userCond+`
		ON CONFLICT DO NOTHING`,
		name, keyHash, s.now().Unix(), user)
	if err != nil {
		return DeviceInfo{}, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := s.db.Write.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE `+userCond, user).Scan(&exists); err != nil {
			return DeviceInfo{}, "", err
		}
		if exists == 0 {
			return DeviceInfo{}, "", ErrUserNotFound
		}
		return DeviceInfo{}, "", ErrDeviceExists
	}
	id, _ := res.LastInsertId()
	d, err := s.deviceByID(ctx, Scope{All: true}, id)
	if err != nil {
		return DeviceInfo{}, "", err
	}
	s.forgetRejected(d.Owner)
	return d, password, nil
}

const devicesQuery = `
	SELECT d.id, d.name, u.username, d.created_at, d.last_used_at, d.revoked_at,
	       (SELECT e.device FROM progress_events e
	         WHERE e.credential_id = d.id ORDER BY e.id DESC LIMIT 1)
	FROM device_credentials d JOIN users u ON u.id = d.user_id
	WHERE (? OR d.user_id = ?)`

// Devices возвращает устройства области, включая отозванные, по владельцу и
// времени создания.
func (s *Service) Devices(ctx context.Context, sc Scope) ([]DeviceInfo, error) {
	rows, err := s.db.Read.QueryContext(ctx, devicesQuery+` ORDER BY u.username, d.id`, sc.All, sc.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceInfo
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// deviceByID читает устройство области через соединение записи: сразу после
// изменения, в том же порядке, что и запись.
func (s *Service) deviceByID(ctx context.Context, sc Scope, id int64) (DeviceInfo, error) {
	d, err := scanDevice(s.db.Write.QueryRowContext(ctx, devicesQuery+` AND d.id = ?`, sc.All, sc.UserID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceInfo{}, ErrNotFound
	}
	return d, err
}

func scanDevice(row interface{ Scan(...any) error }) (DeviceInfo, error) {
	var d DeviceInfo
	var created int64
	var lastUsed, revoked sql.NullInt64
	var reported sql.NullString
	if err := row.Scan(&d.ID, &d.Name, &d.Owner, &created, &lastUsed, &revoked, &reported); err != nil {
		return DeviceInfo{}, err
	}
	d.CreatedAt = time.Unix(created, 0)
	d.LastUsedAt = optTime(lastUsed)
	d.RevokedAt = optTime(revoked)
	if reported.Valid {
		d.ReportedDevice = &reported.String
	}
	return d, nil
}

// RenameDeviceByID переименовывает действующее устройство области; ключ не
// меняется. Отозванное устройство — ErrNotFound.
func (s *Service) RenameDeviceByID(ctx context.Context, sc Scope, id int64, name string) (DeviceInfo, error) {
	name, err := normalizeDeviceName(name)
	if err != nil {
		return DeviceInfo{}, err
	}
	// OR IGNORE: конфликт с частичным уникальным индексом имён даёт ноль строк,
	// а не ошибку; причину различаем чтением ниже.
	res, err := s.db.Write.ExecContext(ctx, `
		UPDATE OR IGNORE device_credentials SET name = ?
		WHERE id = ? AND revoked_at IS NULL AND (? OR user_id = ?)`,
		name, id, sc.All, sc.UserID)
	if err != nil {
		return DeviceInfo{}, err
	}
	d, err := s.deviceByID(ctx, sc, id)
	if err != nil {
		return DeviceInfo{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if d.RevokedAt != nil {
			return DeviceInfo{}, ErrNotFound
		}
		return DeviceInfo{}, ErrDeviceExists
	}
	return d, nil
}

// ResetDeviceKey выдаёт действующему устройству области новый пароль. key_rev
// растёт, как в RotateKey, поэтому закешированные проверки старого ключа
// перестают действовать сразу; id и связь с журналом сохраняются.
func (s *Service) ResetDeviceKey(ctx context.Context, sc Scope, id int64) (DeviceInfo, string, error) {
	password := GeneratePassword(2)
	keyHash, err := s.hash(KeyFromPassword(password))
	if err != nil {
		return DeviceInfo{}, "", err
	}
	res, err := s.db.Write.ExecContext(ctx, `
		UPDATE device_credentials SET key_hash = ?, key_rev = key_rev + 1
		WHERE id = ? AND revoked_at IS NULL AND (? OR user_id = ?)`,
		keyHash, id, sc.All, sc.UserID)
	if err := affected(res, err); err != nil {
		return DeviceInfo{}, "", err
	}
	d, err := s.deviceByID(ctx, sc, id)
	if err != nil {
		return DeviceInfo{}, "", err
	}
	s.forgetRejected(d.Owner)
	return d, password, nil
}

// RevokeDeviceByID отзывает устройство области. Повторный отзыв ничего не меняет.
func (s *Service) RevokeDeviceByID(ctx context.Context, sc Scope, id int64) error {
	res, err := s.db.Write.ExecContext(ctx, `
		UPDATE device_credentials SET revoked_at = coalesce(revoked_at, ?)
		WHERE id = ? AND (? OR user_id = ?)`,
		s.now().Unix(), id, sc.All, sc.UserID)
	return affected(res, err)
}
