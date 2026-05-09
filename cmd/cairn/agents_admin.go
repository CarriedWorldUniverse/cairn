package cairn

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
)

// AgentsList prints the current user's agents as a table to out.
// status is "" for all, or "pending" / "active".
func AgentsList(instanceURL, status string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}

	c := NewClient(instanceURL, token)
	agents, err := c.ListAgents(context.Background(), status)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tDOMAIN\tSTATUS\tBLOCKED\tFINGERPRINT")
	for _, a := range agents {
		blocked := "no"
		if a.Blocked {
			blocked = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Slug, a.Domain, a.Status, blocked, a.Fingerprint)
	}
	return tw.Flush()
}

// AgentsApprove approves a pending agent. Owner-only on the server.
func AgentsApprove(instanceURL, fingerprint string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}
	c := NewClient(instanceURL, token)
	if err := c.Approve(context.Background(), fingerprint); err != nil {
		return err
	}
	fmt.Fprintf(out, "approved: %s\n", fingerprint)
	return nil
}

// AgentsBlock adds an agent to the blocklist with a reason. Owner-only.
func AgentsBlock(instanceURL, fingerprint, reason string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}
	c := NewClient(instanceURL, token)
	if err := c.Block(context.Background(), fingerprint, reason); err != nil {
		return err
	}
	fmt.Fprintf(out, "blocked: %s (reason: %s)\n", fingerprint, reason)
	return nil
}
