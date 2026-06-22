package change

// Line is a row in the line catalogue: a named history (like a branch) with a
// tip commit, a fork-point base commit, and an optional parent line.
type Line struct {
	ID         string
	Name       string
	ParentLine string
	TipCommit  string
	BaseCommit string
	Status     string
}
