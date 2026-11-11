package change

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// cutReflogRev splits "<line>@{N}" (also "HEAD@{N}" and a bare "@{N}") into
// the line name and N.
func cutReflogRev(name string) (line string, n int, ok bool) {
	i := strings.LastIndex(name, "@{")
	if i < 0 || !strings.HasSuffix(name, "}") {
		return "", 0, false
	}
	n, err := strconv.Atoi(name[i+2 : len(name)-1])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return name[:i], n, true
}

// ReflogTips lists the tips a line has had, newest first — the entries
// `cairn reflog <line>` prints, which are the operations that moved the
// line's tip (working-copy autosaves excluded), as recorded: the tip commit
// after each, which may be the open working change's placeholder.
// resolveReflogRev looks through that to its parent, as a line name is
// everywhere.
func (e *Engine) ReflogTips(line string) ([]string, error) {
	ops, err := e.OperationLog()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, op := range ops {
		if op.OpType == "snapshot" {
			continue
		}
		before, bok := op.ViewBefore[line]
		after, aok := op.ViewAfter[line]
		if bok == aok && before == after {
			continue
		}
		out = append(out, after)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// resolveReflogRev resolves <line>@{N}: where the line's tip was N moves ago,
// per the operation log.
func (e *Engine) resolveReflogRev(lineName string, n int) (plumbing.Hash, error) {
	if lineName == "" || lineName == "HEAD" || lineName == "@" {
		l, err := e.HeadLine()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		lineName = l.Name
	} else if _, err := e.LineByName(lineName); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("%q is not a line: %w", lineName, ErrNotFound)
	}
	tips, err := e.ReflogTips(lineName)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if n >= len(tips) {
		return plumbing.ZeroHash, fmt.Errorf("log for %q only has %d entries: %w", lineName, len(tips), ErrNotFound)
	}
	sha := tips[n]
	if sha == "" {
		return plumbing.ZeroHash, fmt.Errorf("line %q had no commit then: %w", lineName, ErrNoCommits)
	}
	if c, err := e.git.CommitObject(plumbing.NewHash(sha)); err == nil &&
		strings.TrimSpace(stripChangeID(c.Message)) == workingDescription && len(c.ParentHashes) > 0 {
		return c.ParentHashes[0], nil
	}
	return plumbing.NewHash(sha), nil
}
