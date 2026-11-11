package main

import (
	_ "embed"
	"fmt"
	"strings"
)

// helpAgentText is the static page for LLM/agent users (`cairn help agent`).
// {{VERSION}} on the first line is replaced with buildVersion at print time.
//
//go:embed help_agent.txt
var helpAgentText string

// agentHelpDiscovery is the last line of the plain usage output.
const agentHelpDiscovery = "LLM/agent users: run 'cairn help agent' for a compact guide written for you."

// isAgentHelpTopic reports whether `help <topic>` names the agent page.
func isAgentHelpTopic(topic string) bool {
	switch topic {
	case "agent", "agents", "llm":
		return true
	}
	return false
}

// wantsAgentHelp reports whether args (os.Args[1:] after the global flags) are
// `help agent|agents|llm`. That page is always plain stdout, never paged.
func wantsAgentHelp(args []string) bool {
	return len(args) == 2 && args[0] == "help" && isAgentHelpTopic(args[1])
}

// printAgentHelp prints the agent page, stamped with this binary's version.
func printAgentHelp() {
	fmt.Print(strings.Replace(helpAgentText, "{{VERSION}}", buildVersion, 1))
}
