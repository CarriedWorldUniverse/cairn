package change

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

const originRemote = "origin"

// fetchRemote ensures an "origin" remote at url and fetches all heads + tags
// into the bare store. Idempotent (re-fetch is fine).
func (e *Engine) fetchRemote(url string) error {
	rem, err := e.git.Remote(originRemote)
	if errors.Is(err, git.ErrRemoteNotFound) {
		rem, err = e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}})
	}
	if err != nil {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	err = rem.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
		},
		Tags: git.AllTags,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	return nil
}

// detectDefault returns the remote's default branch short name.
//
// It first asks the remote for its advertised refs and looks for the HEAD
// symbolic reference, which names the default branch directly. Over file://
// transports go-git's Remote.List does not reliably surface a symbolic HEAD
// (the local transport advertises HEAD as a plain hash, not a symref), so we
// also fall back to the fetched heads: "main" if present, else a sole head,
// else an error. A freshly-Open'd cairn bare repo has its own HEAD (pointing at
// the local root line), so we never read e.git's local HEAD here — only the
// remote's advertised HEAD and the fetched remote heads are trusted.
func (e *Engine) detectDefault() (string, error) {
	rem, err := e.git.Remote(originRemote)
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	refs, err := rem.List(&git.ListOptions{})
	if err == nil {
		for _, ref := range refs {
			if ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference {
				return ref.Target().Short(), nil
			}
		}
	}
	// Fallback: the remote did not advertise a symbolic HEAD (common over
	// file://). Use the fetched heads.
	heads, err := e.listHeads()
	if err != nil {
		return "", err
	}
	if _, ok := heads["main"]; ok {
		return "main", nil
	}
	if len(heads) == 1 {
		for name := range heads {
			return name, nil
		}
	}
	return "", fmt.Errorf("change.detectDefault: cannot determine default branch")
}

// listHeads returns short-name → commit-sha for refs/heads/* in the store.
func (e *Engine) listHeads() (map[string]string, error) {
	return e.listRefs("refs/heads/")
}

// listTags returns short-name → commit-sha for refs/tags/* in the store.
func (e *Engine) listTags() (map[string]string, error) {
	return e.listRefs("refs/tags/")
}

// listRefs returns short-name → sha for every hash reference whose full name
// begins with prefix. It mirrors export.go's IterReferences iteration style.
func (e *Engine) listRefs(prefix string) (map[string]string, error) {
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("change.listRefs: %w", err)
	}
	defer iter.Close()
	out := map[string]string{}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		n := ref.Name().String()
		if len(n) > len(prefix) && n[:len(prefix)] == prefix {
			out[n[len(prefix):]] = ref.Hash().String()
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("change.listRefs: %w", err)
	}
	return out, nil
}
