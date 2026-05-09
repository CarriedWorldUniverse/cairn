// Cairn-specific code; AGPLv3. See LICENSING.md.

package setting

// Cairn holds Cairn-specific configuration loaded from app.ini's
// [cairn] section.
//
// All keys are optional; defaults below match the safer/conservative
// posture for a fresh deploy. Operators who want stricter behaviour
// (e.g. EnforceSignatures=true once the agent fleet is bootstrapped)
// should flip the relevant flag in app.ini.
var Cairn = struct {
	Enabled                      bool
	EnforceSignatures            bool
	RejectOrphanAgents           bool
	HMACKeyPath                  string
	MarkdownEndpointsEnabled     bool
	SummarizerEnabled            bool
	WALCheckpointIntervalMinutes int
}{
	Enabled:                      true,
	EnforceSignatures:            false,
	RejectOrphanAgents:           true,
	HMACKeyPath:                  "/etc/cairn/instance-hmac.key",
	MarkdownEndpointsEnabled:     true,
	SummarizerEnabled:            true,
	WALCheckpointIntervalMinutes: 5,
}

func loadCairnFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("cairn")
	Cairn.Enabled = sec.Key("enabled").MustBool(true)
	Cairn.EnforceSignatures = sec.Key("enforce_signatures").MustBool(false)
	Cairn.RejectOrphanAgents = sec.Key("reject_orphan_agents").MustBool(true)
	Cairn.HMACKeyPath = sec.Key("hmac_key_path").MustString("/etc/cairn/instance-hmac.key")
	Cairn.MarkdownEndpointsEnabled = sec.Key("markdown_endpoints_enabled").MustBool(true)
	Cairn.SummarizerEnabled = sec.Key("summarizer_enabled").MustBool(true)
	Cairn.WALCheckpointIntervalMinutes = sec.Key("wal_checkpoint_interval_minutes").MustInt(5)
}
