package worktree

import "github.com/go-git/go-git/v5/config"

// cfgSpec is a refspec string; toRefSpecs converts for the engine.
type cfgSpec string

func toRefSpecs(in []cfgSpec) []config.RefSpec {
	out := make([]config.RefSpec, len(in))
	for i, s := range in {
		out[i] = config.RefSpec(s)
	}
	return out
}
