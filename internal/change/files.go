package change

import "fmt"

// Files returns the path->bytes file map of the tree at the given commit sha.
func (e *Engine) Files(commitSha string) (map[string][]byte, error) {
	tree, err := e.commitTree(commitSha)
	if err != nil {
		return nil, fmt.Errorf("change.Files: %w", err)
	}
	return e.readTree(tree)
}
