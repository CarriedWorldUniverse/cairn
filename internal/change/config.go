package change

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// GetConfig returns the stored value for key. The bool is false (with no error)
// when the key is unset.
func (e *Engine) GetConfig(key string) (string, bool, error) {
	var value string
	err := e.db.QueryRow(`SELECT value FROM config WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("change.GetConfig: %w", err)
	}
	return value, true, nil
}

// SetConfig stores (inserting or updating) value under key.
func (e *Engine) SetConfig(key, value string) error {
	if _, err := e.db.Exec(
		`INSERT INTO config(key, value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value); err != nil {
		return fmt.Errorf("change.SetConfig: %w", err)
	}
	return nil
}

// ConfigTruthy reports whether a stored config value means "on". It accepts
// "true", "1", and "on" (case-insensitive, surrounding whitespace trimmed).
func ConfigTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "on":
		return true
	default:
		return false
	}
}
