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

// UnsetConfig removes key; it reports whether the key existed.
func (e *Engine) UnsetConfig(key string) (bool, error) {
	res, err := e.db.Exec(`DELETE FROM config WHERE key=?`, key)
	if err != nil {
		return false, fmt.Errorf("change.UnsetConfig: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AllConfig returns every stored key/value.
func (e *Engine) AllConfig() (map[string]string, error) {
	rows, err := e.db.Query(`SELECT key, value FROM config`)
	if err != nil {
		return nil, fmt.Errorf("change.AllConfig: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("change.AllConfig: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}
