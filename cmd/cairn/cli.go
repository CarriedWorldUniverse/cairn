// Copyright The Forgejo Authors.
// SPDX-License-Identifier: MIT

package cairn

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

// Commands returns the cairn subcommand group for registration with
// Forgejo's main CLI tree (urfave/cli v3).
func Commands() *cli.Command {
	return &cli.Command{
		Name:  "cairn",
		Usage: "Cairn agent administration",
		Commands: []*cli.Command{
			{
				Name:     "auth",
				Usage:    "Authentication",
				Commands: []*cli.Command{authLoginCmd()},
			},
			{
				Name:     "agent",
				Usage:    "Per-agent operations",
				Commands: []*cli.Command{agentAttachCmd()},
			},
			{
				Name:     "agents",
				Usage:    "Owner-side agent admin",
				Commands: []*cli.Command{agentsListCmd(), agentsApproveCmd(), agentsBlockCmd()},
			},
			commitSignHelperCmd(),
		},
	}
}

func flagInstance() *cli.StringFlag {
	return &cli.StringFlag{
		Name:     "instance",
		Usage:    "Cairn instance URL (e.g. https://cairn.darksoft.co.nz)",
		Required: true,
	}
}

func flagSlug() *cli.StringFlag   { return &cli.StringFlag{Name: "slug", Required: true} }
func flagDomain() *cli.StringFlag { return &cli.StringFlag{Name: "domain", Required: true} }

func authLoginCmd() *cli.Command {
	return &cli.Command{
		Name:  "login",
		Usage: "Obtain and store an API token. Password from $CAIRN_PASSWORD or interactive prompt.",
		Flags: []cli.Flag{
			flagInstance(),
			&cli.StringFlag{Name: "username", Required: true},
			&cli.StringFlag{Name: "token-name", Value: "cairn-cli"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			password := os.Getenv("CAIRN_PASSWORD")
			if password == "" {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("no password: set $CAIRN_PASSWORD or run interactively")
				}
				fmt.Fprint(os.Stderr, "Password: ")
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
				password = string(pw)
			}
			return AuthLogin(c.String("instance"), c.String("username"), password, c.String("token-name"))
		},
	}
}

func agentAttachCmd() *cli.Command {
	return &cli.Command{
		Name:  "attach",
		Usage: "Submit an attachment request for an agent-generated keypair",
		Flags: []cli.Flag{
			flagInstance(),
			&cli.StringFlag{Name: "owner", Required: true, Usage: "Proposed owner username"},
			flagSlug(),
			flagDomain(),
			&cli.StringFlag{Name: "pubkey", Required: true, Usage: "Path to the agent's OpenSSH-format public key file (e.g. <slug>.key.pub)"},
			&cli.StringFlag{Name: "token", Usage: "Optional API token; if omitted the request is anonymous and lands in pending status"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return AgentAttach(
				c.String("instance"),
				c.String("owner"),
				c.String("slug"),
				c.String("domain"),
				c.String("pubkey"),
				c.String("token"),
				os.Stdout,
			)
		},
	}
}

func agentsListCmd() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List the current user's agents",
		Flags: []cli.Flag{
			flagInstance(),
			&cli.StringFlag{Name: "status", Usage: "Filter: pending | active"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return AgentsList(c.String("instance"), c.String("status"), os.Stdout)
		},
	}
}

func agentsApproveCmd() *cli.Command {
	return &cli.Command{
		Name:      "approve",
		Usage:     "Approve a pending agent",
		ArgsUsage: "<fingerprint>",
		Flags:     []cli.Flag{flagInstance()},
		Action: func(ctx context.Context, c *cli.Command) error {
			fp := c.Args().First()
			if fp == "" {
				return cli.Exit("fingerprint argument required", 1)
			}
			return AgentsApprove(c.String("instance"), fp, os.Stdout)
		},
	}
}

func agentsBlockCmd() *cli.Command {
	return &cli.Command{
		Name:      "block",
		Usage:     "Add an agent to the blocklist",
		ArgsUsage: "<fingerprint>",
		Flags: []cli.Flag{
			flagInstance(),
			&cli.StringFlag{Name: "reason", Required: true},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			fp := c.Args().First()
			if fp == "" {
				return cli.Exit("fingerprint argument required", 1)
			}
			return AgentsBlock(c.String("instance"), fp, c.String("reason"), os.Stdout)
		},
	}
}

func commitSignHelperCmd() *cli.Command {
	return &cli.Command{
		Name:  "commit-sign-helper",
		Usage: "Git ssh-signing helper (gpg.ssh.program). Compatible with ssh-keygen -Y sign argv.",
		Flags: []cli.Flag{
			// Cairn-specific: instance URL. Falls back to $CAIRN_INSTANCE
			// since git's gpg.ssh.program flow can't easily pass our flags.
			&cli.StringFlag{
				Name:    "instance",
				Sources: cli.EnvVars("CAIRN_INSTANCE"),
				Usage:   "Cairn instance URL (or $CAIRN_INSTANCE)",
			},
			// Optional: explicit slug. If absent, inferred from -f keyfile.
			&cli.StringFlag{Name: "slug", Usage: "Agent slug (else inferred from -f keyfile)"},
			// ssh-keygen-compatible flags (git invokes us with these):
			&cli.StringFlag{Name: "Y", Usage: "ssh-keygen mode (ignored; always sign)"},
			&cli.StringFlag{Name: "n", Usage: "Signature namespace"},
			&cli.StringFlag{Name: "f", Usage: "Key file path (slug inferred from filename)"},
			// Legacy flag retained for direct invocation:
			&cli.StringFlag{Name: "namespace", Value: "git"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			instance := c.String("instance")
			if instance == "" {
				return fmt.Errorf("--instance or $CAIRN_INSTANCE required")
			}

			// Resolve slug: explicit --slug > inferred from -f keyfile.
			slug := c.String("slug")
			if slug == "" {
				if keyfile := c.String("f"); keyfile != "" {
					slug = inferSlugFromKeyfile(keyfile)
				}
			}
			if slug == "" {
				return fmt.Errorf("--slug or -f keyfile required")
			}

			// Resolve namespace: -n (git's flag) > --namespace (default "git").
			namespace := c.String("n")
			if namespace == "" {
				namespace = c.String("namespace")
			}

			return CommitSignHelper(instance, slug, namespace, os.Stdin, os.Stdout)
		},
	}
}
