// Cairn-specific code; AGPLv3. See LICENSING.md.

package identity

import "sync/atomic"

// globalService holds the process-wide AgentService set by SetGlobal at
// server startup. The atomic.Pointer makes the read path on the hot
// hook code path lock-free; SetGlobal is called once during Init.
//
// The global lives here (next to the type) rather than in the v1 router
// package so that consumers outside the API layer — notably the
// pre-receive hook in routers/private — can reach the service without
// creating a routers-imports-routers dependency.
var globalService atomic.Pointer[AgentService]

// SetGlobal stores the process-wide AgentService. It is intended to be
// called exactly once at startup, before any reader can run. Calling
// it again replaces the previous service (useful for tests).
func SetGlobal(svc *AgentService) {
	globalService.Store(svc)
}

// GlobalAgentService returns the AgentService stored by SetGlobal, or
// nil if none has been set. The hook code path treats nil as "Cairn
// not initialised — fail closed when enforcement is on".
func GlobalAgentService() *AgentService {
	return globalService.Load()
}
