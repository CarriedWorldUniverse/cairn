package change

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

const originRemote = "origin"

// ImportFromRemote fetches url's refs into the bare store and maps them onto the
// change-graph. When the remote advertises a cairn meta ref (refs/cairn/meta) the
// EXACT change-graph (line tree, changes, conflicts) is reconstructed from the meta
// commit with full fidelity (see importMeta). Otherwise it falls back to a lossy
// flat projection of refs/heads: the remote default branch becomes this engine's
// root line (the unique parent_line IS NULL row, renamed in place) and every other
// head becomes a flat child line off the root. On both paths every tag is recorded.
// It is idempotent — re-importing the same remote re-fetches and upserts without
// creating duplicate lines or tags. Returns the default branch short name.
func (e *Engine) ImportFromRemote(url string) (string, error) {
	if err := e.fetchRemote(url); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	tags, err := e.listTags()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}

	// If the remote advertised a cairn meta ref (refs/cairn/meta), the full
	// change-graph was fetched and we reconstruct it with FULL FIDELITY. Otherwise
	// we fall back to the lossy flat projection of refs/heads.
	metaRef, metaErr := e.git.Reference(plumbing.ReferenceName("refs/cairn/meta"), false)
	hasMeta := metaErr == nil && metaRef != nil

	tx, err := e.db.Begin()
	if err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ts := e.now().UTC().Format(time.RFC3339Nano)

	var def string
	if hasMeta {
		// Fidelity path: importMeta DELETEs the init catalogue and re-installs the
		// exact line tree / changes / conflicts from the meta commit. It returns the
		// reconstructed root line (the unique parent_line IS NULL row).
		def, err = e.importMeta(metaRef.Hash().String(), tx, ts)
		if err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}
	} else {
		// Flat-projection path (no cairn metadata on the remote).
		// detectDefault opens a SECOND network round-trip (the ref
		// advertisement), so it is announced before it blocks rather than
		// after — on a slow remote it is the longest silent phase there is.
		e.Progressf("cairn: resolving the remote's default branch …\n")
		def, err = e.detectDefault()
		if err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}
		heads, err := e.listHeads()
		if err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}

		// The root line is the unique parent_line IS NULL row.
		var rootID string
		if err := tx.QueryRow(`SELECT id FROM line WHERE parent_line IS NULL`).Scan(&rootID); err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}

		// Rename the root to the default branch and set it to that head's commit.
		defTip, ok := heads[def]
		if !ok {
			return "", fmt.Errorf("change.ImportFromRemote: default branch %q not in fetched heads", def)
		}
		if _, err := tx.Exec(
			`UPDATE line SET name=?, tip_commit=?, base_commit=?, updated_at=? WHERE id=?`,
			def, defTip, defTip, ts, rootID); err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}

		// Every non-default head becomes a flat child line off the root. Its base
		// is the FORK COMMIT — the merge-base with the default branch — not the
		// branch's own tip. The base is a COMMIT, not a branch: it's the commit the
		// branch diverged from, which lives in the default branch (a valid parent)
		// whenever the intervening branch has merged there. This makes `ahead` the
		// real divergence from trunk rather than "commits since clone". Unrelated
		// histories (no common ancestor) fall back to the tip.
		mapped, toMap := 0, len(heads)-1
		for name, sha := range heads {
			if name == def {
				continue
			}
			// One mergeBase per branch against the default tip — the cost
			// grows with the branch count, and on a repo with hundreds it is
			// minutes. Count it out loud.
			mapped++
			e.Progressf("\rcairn: mapping branches onto the line tree … %d/%d", mapped, toMap)
			base := sha
			if mb, mberr := e.mergeBase(sha, defTip); mberr == nil && mb != "" {
				base = mb
			}
			var existingID string
			err := tx.QueryRow(`SELECT id FROM line WHERE name=?`, name).Scan(&existingID)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := tx.Exec(
					`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
					 VALUES(?,?,?,?,?,'open',?,?)`,
					newID(), name, rootID, sha, base, ts, ts); err != nil {
					return "", fmt.Errorf("change.ImportFromRemote: %w", err)
				}
			case err != nil:
				return "", fmt.Errorf("change.ImportFromRemote: %w", err)
			default:
				if _, err := tx.Exec(
					`UPDATE line SET tip_commit=?, base_commit=?, updated_at=? WHERE id=?`,
					sha, base, ts, existingID); err != nil {
					return "", fmt.Errorf("change.ImportFromRemote: %w", err)
				}
			}
		}
		if toMap > 0 {
			e.Progressf("\n")
		}
	}

	if len(tags) > 0 {
		e.Progressf("cairn: recording %d tag(s) …\n", len(tags))
	}

	// Record each tag (name is PRIMARY KEY; upsert the commit on re-import).
	// Tags are not carried in the meta document, so they are recorded from
	// refs/tags/* on BOTH paths.
	for name, sha := range tags {
		if _, err := tx.Exec(
			`INSERT INTO tag(name, commit_sha, tagger, at) VALUES(?,?,?,?)
			 ON CONFLICT(name) DO UPDATE SET commit_sha=excluded.commit_sha`,
			name, sha, "import", ts); err != nil {
			return "", fmt.Errorf("change.ImportFromRemote: %w", err)
		}
	}

	// Every line created above arrived FROM the remote (this engine is freshly
	// created for the clone), so mark them remote-tracked. This is the signal the
	// fold guard uses to warn before diverging an upstream branch locally; lines
	// created later with `express` stay local (tracks_remote defaults to 0). The
	// mark follows INCOMING refs only — pushing a line never sets it.
	if _, err := tx.Exec(`UPDATE line SET tracks_remote=1, updated_at=?`, ts); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("change.ImportFromRemote: %w", err)
	}
	return def, nil
}

// fetchRemote ensures an "origin" remote at url and fetches all heads + tags
// into the bare store. Idempotent (re-fetch is fine).
func (e *Engine) fetchRemote(url string) error {
	url = storeAndStrip(url) // never persist credentials in the repo remote; move them to the user-level credstore
	rem, err := e.git.Remote(originRemote)
	if errors.Is(err, git.ErrRemoteNotFound) {
		rem, err = e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}})
	} else if err == nil {
		// origin already exists. If its configured URL differs from url, re-point
		// it at the new URL (delete + recreate) so a re-fetch with a changed URL
		// does not silently keep using the old one.
		cfg := rem.Config()
		if len(cfg.URLs) == 0 || cfg.URLs[0] != url {
			if err = e.git.DeleteRemote(originRemote); err != nil {
				return fmt.Errorf("change.fetchRemote: %w", err)
			}
			rem, err = e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}})
		}
	}
	if err != nil {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	// Snapshot before the fetch — see fetchTracking's identical defense in
	// sync.go for why: the "+refs/cairn/*:refs/cairn/*" refspec below also
	// matches refs/cairn/push/<op-id>/* (go-git's glob has no "/" boundary
	// awareness), so a clone/import from a polluted remote could otherwise
	// import a foreign pin ref straight into the new local store.
	before, serr := e.pushPinRefNames()
	if serr != nil {
		return fmt.Errorf("change.fetchRemote: %w", serr)
	}
	err = rem.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			// Also record remote-tracking refs (like `git clone` does) so the
			// repo knows what is already on origin — e.g. `cairn private` warns
			// when a path being withheld is already pushed.
			"+refs/heads/*:refs/remotes/" + originRemote + "/*",
			"+refs/tags/*:refs/tags/*",
			"+refs/cairn/*:refs/cairn/*",
		},
		Tags:     git.AllTags,
		Auth:     auth,
		Progress: e.progress,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.fetchRemote: %w", err)
	}
	if perr := e.pruneImportedPushPins(before, rem, auth); perr != nil {
		return fmt.Errorf("change.fetchRemote: %w", perr)
	}
	return nil
}

// detectDefault returns the remote's default branch short name.
//
// The remote's advertised HEAD symbolic reference is the ONLY authoritative
// answer, so it is the only one trusted: it names the default branch directly,
// and go-git reconstructs it even from a server too old to advertise the symref
// capability (packp.AdvRefs.resolveHead). A freshly-Open'd cairn bare repo has
// its own HEAD (pointing at the local root line), so e.git's local HEAD is
// never read here — only the remote's advertisement and the fetched heads.
//
// Without that answer the fetched heads are the only evidence left, and a SOLE
// head is the one unambiguous reading of them. Beyond that it depends on WHY
// the answer is missing, and the two cases are not the same (#142):
//   - the round-trip FAILED — the remote has an answer we did not get, so any
//     pick can silently contradict it. Refuse. This is the reported bug: go-git
//     capped the advertisement at 10s, a large repo overran it, and the old code
//     guessed "main" — a develop-default repo cloned to the wrong branch while
//     reporting success, which reads as a merely incomplete checkout.
//   - the remote ANSWERED and has no default (an unborn or dangling HEAD, as on
//     a bare push target) — nothing left to contradict, so a conventional trunk
//     name is the best available reading. Take it, and warn that it was a guess.
func (e *Engine) detectDefault() (string, error) {
	rem, err := e.git.Remote(originRemote)
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return "", fmt.Errorf("change.detectDefault: %w", err)
	}
	refs, listErr := listAdvertisedRefs(rem, auth)
	if listErr == nil {
		for _, ref := range refs {
			if ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference {
				return ref.Target().Short(), nil
			}
		}
	}

	heads, err := e.listHeads()
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(heads))
	for name := range heads {
		names = append(names, name)
	}
	sort.Strings(names)

	// A sole head is the default by construction — the one reading of the
	// fetched refs that involves no guess at all.
	if len(names) == 1 {
		return names[0], nil
	}

	// The round-trip FAILED: the remote has an answer we simply did not get, so
	// any pick here can silently contradict it. Refuse. This is the #142 path —
	// go-git's hidden 10s List cap turned a large repo's slow advertisement into
	// a guess of "main", and a develop-default repo cloned to the wrong branch
	// while reporting success.
	if listErr != nil {
		return "", fmt.Errorf(
			"change.detectDefault: cannot determine the remote's default branch: asking the remote for its refs failed (%v), and the %d fetched branches (%s) give no unambiguous answer — re-run the clone rather than check out a branch picked at random",
			listErr, len(names), strings.Join(names, ", "))
	}

	// The remote answered and has no default branch to give: its HEAD is unborn
	// or dangling (a bare repo used only as a push target is the common case).
	// There is no remote answer left to contradict, so fall back to the
	// conventional trunk names — but SAY SO, because this one is a guess.
	for _, conventional := range []string{"main", "master", "trunk", "develop"} {
		if _, ok := heads[conventional]; ok {
			warnf("%s advertises no default branch (its HEAD is unset or dangling); treating %q as the root line, out of %s",
				originRemote, conventional, strings.Join(names, ", "))
			return conventional, nil
		}
	}
	return "", fmt.Errorf(
		"change.detectDefault: cannot determine the remote's default branch: it advertises no default (its HEAD is unset or dangling) and none of the %d fetched branches (%s) is a conventional trunk name — point the remote's HEAD at a branch, or clone and express the branch you want",
		len(names), strings.Join(names, ", "))
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
