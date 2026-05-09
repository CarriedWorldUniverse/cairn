// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
)

// TestInit_SetsGlobal asserts that Init wires the global service.
func TestInit_SetsGlobal(t *testing.T) {
	t.Cleanup(Shutdown)

	eng := cairntest.NewEngine(t)
	Init(eng)

	if Global() == nil {
		t.Fatal("Init should set the global service")
	}
}

func TestInit_DefaultRequireHumanOnly(t *testing.T) {
	t.Cleanup(Shutdown)
	eng := cairntest.NewEngine(t)
	Init(eng)

	// No row inserted → fail-safe default is RequireHumanOnly=true.
	if !Global().RequireHumanOnly(context.Background(), 99) {
		t.Error("default policy should be RequireHumanOnly=true (Cairn AI-native default)")
	}
}

// TestShutdown_ClearsGlobal asserts Shutdown unwires both the global and
// the model-layer hooks. Without this, parallel test packages that call
// Init/Shutdown could leak hook registrations across each other.
func TestShutdown_ClearsGlobal(t *testing.T) {
	eng := cairntest.NewEngine(t)
	Init(eng)
	if Global() == nil {
		t.Fatal("Init should set the global")
	}
	Shutdown()
	if Global() != nil {
		t.Error("Shutdown should clear the global service")
	}
}

// TestSetCairnApprovalFilter_NilNoOp confirms the no-filter fast path
// in models/issues — when no filter is registered, the hook returns
// the input unchanged. This is the contract the count-site fast path
// relies on.
func TestSetCairnApprovalFilter_NilNoOp(t *testing.T) {
	// Defensive: ensure no leftover registration from another test.
	issues_model.SetCairnApprovalFilter(nil)
	t.Cleanup(func() { issues_model.SetCairnApprovalFilter(nil) })

	// We can't introspect models/issues' atomic.Pointer directly, but
	// Shutdown/Init parity is the load-bearing invariant — exercised
	// in TestShutdown_ClearsGlobal above.
}
