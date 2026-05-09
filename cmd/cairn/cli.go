// Copyright The Forgejo Authors.
// SPDX-License-Identifier: MIT

package cairn

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"
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
				Commands: []*cli.Command{agentInitCmd(), agentSubmitCmd()},
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
		Usage: "Obtain and store an API token",
		Flags: []cli.Flag{
			flagInstance(),
			&cli.StringFlag{Name: "username", Required: true},
			&cli.StringFlag{Name: "password", Required: true},
			&cli.StringFlag{Name: "token-name", Value: "cairn-cli"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return AuthLogin(c.String("instance"), c.String("username"), c.String("password"), c.String("token-name"))
		},
	}
}

func agentInitCmd() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "Generate an agent keypair locally",
		Flags: []cli.Flag{flagInstance(), flagSlug(), flagDomain()},
		Action: func(ctx context.Context, c *cli.Command) error {
			return AgentInit(c.String("instance"), c.String("slug"), c.String("domain"), os.Stdout)
		},
	}
}

func agentSubmitCmd() *cli.Command {
	return &cli.Command{
		Name:  "submit",
		Usage: "Register an agent with Cairn",
		Flags: []cli.Flag{
			flagInstance(),
			&cli.StringFlag{Name: "owner", Required: true},
			flagSlug(),
			flagDomain(),
			&cli.BoolFlag{Name: "anonymous", Usage: "Submit without a token (lands in pending status)"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.Bool("anonymous") {
				return AgentSubmitAnonymous(c.String("instance"), c.String("owner"), c.String("slug"), c.String("domain"), os.Stdout)
			}
			return AgentSubmit(c.String("instance"), c.String("owner"), c.String("slug"), c.String("domain"), os.Stdout)
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
		Usage: "Git ssh-signing helper (gpg.ssh.program)",
		Flags: []cli.Flag{
			flagInstance(),
			flagSlug(),
			&cli.StringFlag{Name: "namespace", Value: "git"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return CommitSignHelper(c.String("instance"), c.String("slug"), c.String("namespace"), os.Stdin, os.Stdout)
		},
	}
}
