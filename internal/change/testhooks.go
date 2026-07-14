package change

// This file exposes TEST-ONLY seams for issue #98 Phase B's acceptance
// tests: they let a test simulate network latency (a local bare-repo remote
// has no real latency to exercise "the network phase runs without the
// caller's lock") or a push that "succeeds" without landing (issue #117's
// failure mode), without reaching into unexported package state from
// another package. The underlying hooks (testNetworkDelay, testFetchDelay,
// testSkipRealPush) stay unexported; only these setters are exported, and
// only for tests to call.
//
// WARNING: every setter in this file installs a PACKAGE-GLOBAL hook (there is
// no per-Engine scoping) that changes real push/fetch behavior — a delay hook
// blocks the network phase, and SetSkipRealPushHook makes NetworkPush skip
// the actual push RPC entirely. They exist ONLY for cross-package tests
// (internal/worktree) that cannot reach this package's unexported test
// vars directly. Production code (cmd/, internal/worktree non-test files,
// internal/repo, etc.) MUST NEVER call these — doing so would silently alter
// push/fetch behavior for every caller sharing the process, not just the
// caller. Tests that use them must always defer the returned restore func so
// the hook does not leak into unrelated tests (they run process-wide and are
// not safe for t.Parallel() against each other).
//
// A build-tag gate (e.g. `//go:build cairntest`) was considered for this
// file to keep it out of production builds entirely, but does not work here:
// `go test ./internal/worktree/...` builds internal/change as an ordinary
// (non-test) dependency with the caller's default build tags, not
// internal/change's own `_test.go` build constraints — a cross-package test
// caller cannot pull in code gated behind a tag it doesn't also pass, and
// wiring that tag through every `go test` invocation across the module
// (including CI) is more fragile than the setters being globally exported.
// The exported-but-documented-danger shape here is the accepted tradeoff;
// see the WARNING above and internal/change's package-level review notes for
// this rationale.

// SetNetworkDelayHook installs fn to run in NetworkPush just before the
// actual push RPC, simulating a slow remote. Returns a restore func the
// caller must defer to remove the hook (nil restores to no-op).
func SetNetworkDelayHook(fn func()) (restore func()) {
	prev := testNetworkDelay
	testNetworkDelay = fn
	return func() { testNetworkDelay = prev }
}

// SetFetchDelayHook installs fn to run in fetchTracking just before the
// actual fetch RPC, simulating a slow remote. Returns a restore func the
// caller must defer to remove the hook (nil restores to no-op).
func SetFetchDelayHook(fn func()) (restore func()) {
	prev := testFetchDelay
	testFetchDelay = fn
	return func() { testFetchDelay = prev }
}

// SetSkipRealPushHook installs fn, consulted by NetworkPush to decide
// whether to skip the actual push RPC while still running post-push
// verification — simulating issue #117's failure mode (go-git's file
// transport reporting success without the ref landing). Returns a restore
// func the caller must defer to remove the hook.
func SetSkipRealPushHook(fn func() bool) (restore func()) {
	prev := testSkipRealPush
	testSkipRealPush = fn
	return func() { testSkipRealPush = prev }
}
