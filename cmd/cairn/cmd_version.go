package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/credstore"
	"github.com/CarriedWorldUniverse/cairn/internal/release"
	"github.com/CarriedWorldUniverse/cairn/internal/selfupdate"
	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

// cmdTag names the tip of a branch with a tag. Usage:
//
//	cairn tag [--repo dir] <name> [branch]
//
// branch defaults to the structural root.
func cmdTag(args []string) error {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn tag <name> [branch]")
	}
	name := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch := ""
	if fs.NArg() >= 2 {
		branch = fs.Arg(1)
	} else {
		branch, err = r.DefaultBranch()
		if err != nil {
			return mapErr(err)
		}
	}
	if err := r.Tag(name, branch); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: tagged %s -> %s\n", branch, name)
	return nil
}

// cmdVersion prints the derived version for the default branch, rendered for
// the requested ecosystem (default: plain semver). Stdout carries the version
// string ONLY so callers can do $(cairn version).
func cmdVersion(args []string) error {
	if len(args) > 0 && args[0] == "bump" {
		return cmdVersionBump(args[1:])
	}
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	target := fs.String("target", "", "render for ecosystem: npm|nuget|pypi|oci|go")
	releaseForm := fs.Bool("release", false, "print the clean release version that `cairn release` would tag")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	cfg, err := version.LoadConfig(r.Root())
	if err != nil {
		return mapErr(err)
	}
	in, err := r.DeriveInput(branch, cfg)
	if err != nil {
		return mapErr(err)
	}
	var v version.Canonical
	if *releaseForm {
		v, err = version.ReleaseVersion(in)
	} else {
		v, err = version.Derive(in)
	}
	if err != nil {
		return mapErr(err)
	}
	out, err := version.Render(v, *target)
	if err != nil {
		return mapErr(err)
	}
	fmt.Println(out)
	return nil
}

// cmdVersionBump records explicit bump intent (major|minor|patch) for the next
// release. The level is positional and must appear before any flags.
func cmdVersionBump(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: cairn version bump major|minor|patch")
	}
	level := args[0]
	switch level {
	case "major", "minor", "patch":
	default:
		return errors.New("usage: cairn version bump major|minor|patch [--repo DIR]")
	}
	fs := flag.NewFlagSet("version bump", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args[1:]); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.SetPendingBump(level); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: next release bump set to %s\n", level)
	return nil
}

// cmdRelease cuts a clean release version (e.g. v1.0.1) for the default branch
// and the requested ecosystem: it derives the next release version, stamps the
// manifest, tags, and publishes atomically (with --dry-run showing the plan).
func cmdRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	target := fs.String("target", "", "ecosystem: npm|nuget|pypi|oci")
	dryRun := fs.Bool("dry-run", false, "show the plan without tagging or publishing")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("usage: cairn release --target npm|nuget|pypi|oci [--dry-run]")
	}
	switch *target {
	case "npm", "nuget", "pypi", "oci":
	default:
		return errors.New("usage: cairn release --target npm|nuget|pypi|oci [--dry-run]")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	cfg, err := version.LoadConfig(r.Root())
	if err != nil {
		return mapErr(err)
	}
	in, err := r.DeriveInput(branch, cfg)
	if err != nil {
		return mapErr(err)
	}
	rel, err := version.ReleaseVersion(in)
	if err != nil {
		return mapErr(err)
	}
	rendered, err := version.Render(rel, *target)
	if err != nil {
		return mapErr(err)
	}
	port, err := r.ReleasePort(branch, *target)
	if err != nil {
		return mapErr(err)
	}
	opts := release.Options{
		Eco:     *target,
		Version: rendered,
		Core:    rel,
		TagName: cfg.TagPrefix + rel.String(),
		Dir:     filepath.Join(*repo, branch),
	}
	if *dryRun {
		plan, err := release.Plan(opts, port, newProbe())
		if err != nil {
			return mapErr(err)
		}
		fmt.Println(plan)
		return nil
	}
	if err := release.Release(opts, port, newPublisher(), newProbe()); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: released %s (%s) tagged %s\n", rendered, *target, opts.TagName)
	if *target == "npm" || *target == "pypi" || *target == "nuget" {
		fmt.Fprintf(os.Stderr, "cairn: manifest stamped but not committed — run `cairn commit %s` before the next release or a pull\n", branch)
	}
	return nil
}

// cmdUpdate replaces the running binary with the latest GitHub release
// (internal/selfupdate: query, checksum-verify, atomic swap). Repo-free: it
// never touches a working copy. The API token (optional — the repo is public;
// it only lifts the anonymous rate limit) resolves like push auth:
// CAIRN_TOKEN > GITHUB_TOKEN > credstore.
func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "report whether a newer release exists, without installing")
	force := fs.Bool("force", false, "install the latest release even if this build is not older (required for dev builds)")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	token := os.Getenv("CAIRN_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = credstore.Get("github.com")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := selfupdate.New(token).Update(ctx, buildVersion, exe, *check, *force)
	if err != nil {
		return err
	}
	switch {
	case res.Updated:
		fmt.Fprintf(os.Stderr, "cairn: updated %s → %s (%s)\n", res.Current, res.Latest, res.Target)
	case *check && res.Newer:
		fmt.Fprintf(os.Stderr, "cairn: %s is available (running %s) — run `cairn update` to install\n", res.Latest, res.Current)
	default:
		fmt.Fprintf(os.Stderr, "cairn: already up to date (%s)\n", res.Current)
	}
	return nil
}
