package change

import (
	"context"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// advertiseTimeout bounds ONE ref-advertisement round-trip.
//
// It exists because go-git's Remote.List silently substitutes a 10-SECOND
// deadline whenever ListOptions.Timeout is left zero (see remote.go's List:
// "Default to the old hardcoded 10s value"). Fetch has no such cap, so on a
// large repository — or a slow link, or a server that enumerates thousands of
// refs before it answers — the whole pack downloads fine and only the much
// smaller advertisement times out. detectDefault swallowed that deadline error
// and guessed "main", checking the WRONG branch out of a repo whose default is
// develop (#142); pruneImportedPushPins and verifyPush turned it into a bogus
// hard failure on an operation that had actually succeeded.
//
// Ten minutes is far past any honest advertisement while still bounding a hung
// connection, and it applies per call — never to the fetch it precedes.
const advertiseTimeout = 10 * time.Minute

// listAdvertisedRefs returns rem's currently advertised refs. It is the ONE
// place cairn asks a remote what it has: it goes through ListContext with an
// explicit deadline so go-git's hidden 10s List default (see advertiseTimeout)
// can never apply. Callers must not fall back to rem.List.
func listAdvertisedRefs(rem *git.Remote, auth transport.AuthMethod) ([]*plumbing.Reference, error) {
	ctx, cancel := context.WithTimeout(context.Background(), advertiseTimeout)
	defer cancel()
	return rem.ListContext(ctx, &git.ListOptions{Auth: auth})
}
