// Package worktree bridges expressed branch folders and the cairn change engine.
package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Entry records the on-disk path of an expressed branch folder and the change
// it currently points at.
type Entry struct {
	Path     string `json:"path"`
	ChangeID string `json:"change_id"`
}

// State is the persisted working-copy state (wc.json), mapping each expressed
// branch name to its Entry.
type State struct {
	Expressed map[string]Entry `json:"expressed"`
}

// LoadState reads working-copy state from path. A missing file is not an error:
// an empty State is returned.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{Expressed: map[string]Entry{}}, nil
		}
		return nil, fmt.Errorf("worktree.LoadState: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("worktree.LoadState: %w", err)
	}
	if s.Expressed == nil {
		s.Expressed = map[string]Entry{}
	}
	return &s, nil
}

// SaveState writes working-copy state to path atomically (write-temp-then-rename).
func SaveState(path string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree.SaveState: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("worktree.SaveState: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("worktree.SaveState: %w", err)
	}
	return nil
}
