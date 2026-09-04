package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
	"google.golang.org/grpc/status"
)

// cmdPR dispatches the `pr` verbs. With no verb it prints usage and errors
// (mirrors `cairn bisect`/`cairn stash`'s no-subcommand behaviour).
func cmdPR(args []string) error {
	if len(args) == 0 {
		fmt.Println(prUsage)
		return errors.New("usage: cairn pr open|list|view|diff|merge")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "open":
		return cmdPROpen(rest)
	case "list":
		return cmdPRList(rest)
	case "view":
		return cmdPRView(rest)
	case "diff":
		return cmdPRDiff(rest)
	case "merge":
		return cmdPRMerge(rest)
	case "help", "-h", "--help":
		fmt.Println(prUsage)
		return nil
	default:
		fmt.Println(prUsage)
		return fmt.Errorf("unknown pr verb %q", verb)
	}
}

// prConnFlags registers the connection/identity flags common to every `pr`
// verb, defaulting from environment then global config (see prUsage).
func prConnFlags(fs *flag.FlagSet) *prConn {
	return &prConn{
		org:      fs.String("org", firstNonEmptyStr(os.Getenv("CAIRN_ORG"), userconfig.Get("pr.org")), "org the repo belongs to"),
		slug:     fs.String("repo-slug", firstNonEmptyStr(os.Getenv("CAIRN_REPO_SLUG"), userconfig.Get("pr.repo-slug")), "repo slug"),
		server:   fs.String("server", firstNonEmptyStr(os.Getenv("CAIRN_GRPC_ADDR"), "127.0.0.1:8102"), "cairn-server gRPC address"),
		subject:  fs.String("subject", firstNonEmptyStr(os.Getenv("CAIRN_SUBJECT"), userconfig.Get("user.email")), "caller identity (cwb-subject)"),
		tlsCert:  fs.String("tls-cert", os.Getenv("CAIRN_TLS_CERT"), "mTLS client certificate (PEM)"),
		tlsKey:   fs.String("tls-key", os.Getenv("CAIRN_TLS_KEY"), "mTLS client key (PEM)"),
		tlsCA:    fs.String("tls-ca", os.Getenv("CAIRN_TLS_CA"), "mTLS CA certificate (PEM)"),
		insecure: fs.Bool("insecure", os.Getenv("CAIRN_DEV_INSECURE") == "1", "skip mTLS (local dev only)"),
	}
}

// cmdPROpen opens a pull request. Reopening the same (repo, source, target)
// returns the existing PR unchanged (server-side idempotency).
func cmdPROpen(args []string) error {
	fs := flag.NewFlagSet("pr open", flag.ContinueOnError)
	conn := prConnFlags(fs)
	project := fs.String("project", "", "ledger project the tracking issue is filed under")
	title := fs.String("m", "", "PR title")
	description := fs.String("description", "", "PR description")
	dod := fs.String("dod", "", "definition of done")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn pr open <source> <target> -m <title> --project <key>")
	}
	source, target := fs.Arg(0), fs.Arg(1)
	if *title == "" {
		return errors.New("pr open: -m <title> required")
	}
	if *project == "" {
		return errors.New("pr open: --project required")
	}
	cli, ctx, err := conn.dial(context.Background(), "repo:write")
	if err != nil {
		return err
	}
	defer cli.Close()
	pull, err := cli.Open(ctx, *conn.org, *conn.slug, source, target, *title, *description, *dod, *project)
	if err != nil {
		return mapPRErr(err)
	}
	printPull(pull)
	return nil
}

// cmdPRList lists a repo's pull requests.
func cmdPRList(args []string) error {
	fs := flag.NewFlagSet("pr list", flag.ContinueOnError)
	conn := prConnFlags(fs)
	state := fs.String("state", "open", "filter: open|merged|all")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	cli, ctx, err := conn.dial(context.Background(), "repo:read")
	if err != nil {
		return err
	}
	defer cli.Close()
	pulls, err := cli.List(ctx, *conn.org, *conn.slug, *state)
	if err != nil {
		return mapPRErr(err)
	}
	for _, p := range pulls {
		printPull(p)
	}
	return nil
}

// cmdPRView shows one pull request.
func cmdPRView(args []string) error {
	fs := flag.NewFlagSet("pr view", flag.ContinueOnError)
	conn := prConnFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn pr view <id>")
	}
	cli, ctx, err := conn.dial(context.Background(), "repo:read")
	if err != nil {
		return err
	}
	defer cli.Close()
	pull, err := cli.View(ctx, *conn.org, *conn.slug, fs.Arg(0))
	if err != nil {
		return mapPRErr(err)
	}
	fmt.Printf("id:       %s\n", pull.GetId())
	fmt.Printf("state:    %s\n", pull.GetState())
	fmt.Printf("branch:   %s -> %s\n", pull.GetSource(), pull.GetTarget())
	fmt.Printf("title:    %s\n", pull.GetTitle())
	fmt.Printf("ledger:   %s\n", pull.GetLedgerIssueKey())
	if pull.GetUrl() != "" {
		fmt.Printf("url:      %s\n", pull.GetUrl())
	}
	return nil
}

// cmdPRDiff prints a PR's unified diff (target...source), the `gh pr diff`
// equivalent: it resolves the PR's branch names server-side (`pr view`), then
// diffs LOCALLY from --remote's tracking refs (fetched read-only, no
// reconcile) — so it works in a clone that has never expressed either line,
// and never needs the gRPC server to compute or store a diff.
func cmdPRDiff(args []string) error {
	fs := flag.NewFlagSet("pr diff", flag.ContinueOnError)
	conn := prConnFlags(fs)
	repoDir, author := repoFlags(fs)
	remote := fs.String("remote", "origin", "git remote to diff tracking refs from (must address the same repo as --org/--repo-slug)")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn pr diff <id>")
	}
	cli, ctx, err := conn.dial(context.Background(), "repo:read")
	if err != nil {
		return err
	}
	defer cli.Close()
	pull, err := cli.View(ctx, *conn.org, *conn.slug, fs.Arg(0))
	if err != nil {
		return mapPRErr(err)
	}

	r, err := openRepo(*repoDir, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	// Pruned fetch: a tracking ref for a branch since deleted on the remote must
	// NOT be left stale (a plain fetch never removes tracking refs) — a deleted
	// PR branch should fail clearly here, not silently diff against its last-
	// known tip.
	if err := r.FetchPruned(*remote); err != nil {
		return mapRemoteErr(err)
	}

	targetRef := "refs/remotes/" + *remote + "/" + pull.GetTarget()
	sourceRef := "refs/remotes/" + *remote + "/" + pull.GetSource()
	// target...source (three-dot / merge-base) semantics, like `gh pr diff`:
	// diffs ONLY what source introduced since it forked from target, so target
	// advancing with commits source never saw never shows up as a spurious
	// revert — a literal tip-to-tip diff (DiffCommits) would get that wrong.
	diffs, err := r.DiffMergeBase(targetRef, sourceRef)
	if errors.Is(err, change.ErrNoCommonAncestor) {
		return fmt.Errorf("pr diff: %s and %s (remote %q) share no common history — nothing to diff",
			pull.GetTarget(), pull.GetSource(), *remote)
	}
	if err != nil {
		return mapErr(fmt.Errorf("pr diff: resolving %s...%s on remote %q (branches %s -> %s): %w",
			pull.GetTarget(), pull.GetSource(), *remote, pull.GetSource(), pull.GetTarget(), err))
	}
	for _, d := range diffs {
		if d.Binary {
			fmt.Printf("Binary files differ: %s\n", d.Path)
			continue
		}
		if d.Unified != "" {
			fmt.Print(d.Unified)
		} else {
			fmt.Printf("%s: %s\n", d.Status, d.Path)
		}
	}
	return nil
}

// cmdPRMerge fast-forward-merges a pull request. A diverged source surfaces
// cairn-server's "not fast-forwardable; rebase X onto Y" guidance unchanged.
func cmdPRMerge(args []string) error {
	fs := flag.NewFlagSet("pr merge", flag.ContinueOnError)
	conn := prConnFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn pr merge <id>")
	}
	cli, ctx, err := conn.dial(context.Background(), "repo:write")
	if err != nil {
		return err
	}
	defer cli.Close()
	res, err := cli.Merge(ctx, *conn.org, *conn.slug, fs.Arg(0))
	if err != nil {
		return mapPRErr(err)
	}
	fmt.Printf("merged %s -> %s @ %s\n", res.GetId(), res.GetTarget(), res.GetMergedSha())
	if res.GetLedgerCommentError() != "" {
		fmt.Fprintf(os.Stderr, "cairn: pull merged, but the ledger comment failed: %s\n", res.GetLedgerCommentError())
	}
	return nil
}

// mapPRErr strips the "rpc error: code = X desc = " envelope from a gRPC
// status error so the server's message (e.g. "not fast-forwardable; rebase
// feature onto main") surfaces to the operator unadorned.
func mapPRErr(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return errors.New(st.Message())
	}
	return err
}
