//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestV502CreateReviewPolicyTable(t *testing.T) {
	eng := cairntest.NewEngine(t)
	exists, err := eng.IsTableExist("cairn_review_policy")
	if err != nil {
		t.Fatalf("IsTableExist: %v", err)
	}
	if !exists {
		t.Error("table cairn_review_policy not created")
	}
}
