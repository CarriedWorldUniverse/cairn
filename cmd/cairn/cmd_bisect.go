package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// cmdBisectRun automates a bisect session by running a test command at each
// materialized midpoint. The command's exit code determines the verdict:
//
//	0   → good
//	125 → skip (untestable midpoint)
//	else → bad
//
// Usage: cairn bisect run [--repo dir] [--author a] -- <cmd> [args...]
//
// A bisect session must already be active (cairn bisect start ...). The session
// stays alive after convergence until the caller runs `cairn bisect reset`.
func cmdBisectRun(args []string) error {
	// Split args at the first "--" token so cairn flags are separated from the
	// test command. Everything before "--" is parsed by FlagSet; everything after
	// is the command to execute.
	var flagArgs, cmdArgs []string
	split := false
	for _, a := range args {
		if a == "--" && !split {
			split = true
			continue
		}
		if split {
			cmdArgs = append(cmdArgs, a)
		} else {
			flagArgs = append(flagArgs, a)
		}
	}
	fs := flag.NewFlagSet("bisect run", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	// Fallback: no "--" separator — treat remaining positional args as the command.
	if len(cmdArgs) == 0 {
		cmdArgs = fs.Args()
	}
	if len(cmdArgs) == 0 {
		return errors.New("usage: cairn bisect run [--repo dir] -- <cmd> [args...]")
	}

	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()

	info, err := r.BisectStatus()
	if err != nil {
		return mapErr(err)
	}
	if !info.Active {
		return errors.New("no bisect in progress; run 'cairn bisect start --good <c> --bad <c>' first")
	}

	// The expressed folder for the bisect branch: <repo>/<branch>.
	folder := filepath.Join(*repo, info.Branch)

	for {
		st, err := r.BisectStatus()
		if err != nil {
			return mapErr(err)
		}
		if st.Done {
			fmt.Println(st.FirstBad)
			fmt.Fprintf(os.Stderr, "cairn: first bad commit: %s — run 'cairn bisect reset' to finish\n", shortSha(st.FirstBad))
			return nil
		}

		// Run the test command in the midpoint folder.
		c := exec.Command(cmdArgs[0], cmdArgs[1:]...) //nolint:gosec
		c.Dir = folder
		c.Stdout = os.Stderr // stream test output to stderr so stdout stays clean
		c.Stderr = os.Stderr
		runErr := c.Run()
		code := 0
		if runErr != nil {
			ee, ok := runErr.(*exec.ExitError)
			if !ok {
				// Non-exit error (e.g. command not found): surface and abort.
				return fmt.Errorf("bisect run: %w", runErr)
			}
			code = ee.ExitCode()
		}

		switch {
		case code == 0:
			fmt.Fprintf(os.Stderr, "cairn: %s — good\n", shortSha(st.Current))
			if _, err := r.BisectMark("good"); err != nil {
				return mapErr(err)
			}
		case code == 125:
			fmt.Fprintf(os.Stderr, "cairn: %s — skip\n", shortSha(st.Current))
			if _, err := r.BisectSkip(); err != nil {
				return mapErr(err)
			}
		default:
			fmt.Fprintf(os.Stderr, "cairn: %s — bad (exit %d)\n", shortSha(st.Current), code)
			if _, err := r.BisectMark("bad"); err != nil {
				return mapErr(err)
			}
		}
	}
}

// cmdBisect dispatches bisect sub-commands. The midpoint to test is materialized
// into the branch folder; auto-snapshot is suspended for the whole session.
func cmdBisect(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cairn bisect start|good|bad|skip|status|reset")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "start":
		return cmdBisectStart(rest)
	case "good":
		return cmdBisectMark(rest, "good")
	case "bad":
		return cmdBisectMark(rest, "bad")
	case "skip":
		return cmdBisectSkip(rest)
	case "status":
		return cmdBisectStatus(rest)
	case "reset":
		return cmdBisectReset(rest)
	case "run":
		return cmdBisectRun(rest)
	default:
		return fmt.Errorf("unknown bisect subcommand %q", sub)
	}
}

// reportBisectStep prints a step's outcome to stderr: the converged first-bad
// commit (with its subject) when Done, else the midpoint to test and how many
// candidates remain.
func reportBisectStep(r *worktree.Repo, step change.BisectStep) error {
	if step.Done {
		fmt.Fprintf(os.Stderr, "cairn: first bad commit: %s\n", step.FirstBad)
		if info, _, err := r.Show(step.FirstBad); err == nil && info.Subject != "" {
			fmt.Fprintf(os.Stderr, "  %s\n", info.Subject)
		}
		return nil
	}
	left := 0
	if info, err := r.BisectStatus(); err == nil {
		left = info.CandidatesLeft
	}
	fmt.Fprintf(os.Stderr, "cairn: testing %s — %d candidates left (mark with 'cairn bisect good' / 'bad')\n", step.Current, left)
	return nil
}

func cmdBisectStart(args []string) error {
	fs := flag.NewFlagSet("bisect start", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	good := fs.String("good", "", "known-good commit")
	bad := fs.String("bad", "", "known-bad commit")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *good == "" || *bad == "" {
		return errors.New("usage: cairn bisect start --good <commit> --bad <commit> [branch]")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	var branch string
	if fs.NArg() > 0 {
		branch = fs.Arg(0)
	} else if branch, err = r.DefaultBranch(); err != nil {
		return mapErr(err)
	}
	step, err := r.BisectStart(branch, *good, *bad)
	if err != nil {
		return mapErr(err)
	}
	return reportBisectStep(r, step)
}

func cmdBisectMark(args []string, verdict string) error {
	fs := flag.NewFlagSet("bisect "+verdict, flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	step, err := r.BisectMark(verdict)
	if err != nil {
		return mapErr(err)
	}
	return reportBisectStep(r, step)
}

func cmdBisectSkip(args []string) error {
	fs := flag.NewFlagSet("bisect skip", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	step, err := r.BisectSkip()
	if err != nil {
		return mapErr(err)
	}
	return reportBisectStep(r, step)
}

func cmdBisectStatus(args []string) error {
	fs := flag.NewFlagSet("bisect status", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	// Read-only: open without a pre-sync so a midpoint/first-bad on disk is never
	// snapshotted into the working change.
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	info, err := r.BisectStatus()
	if err != nil {
		return mapErr(err)
	}
	if !info.Active {
		fmt.Fprintln(os.Stderr, "cairn: no bisect in progress")
		return nil
	}
	fmt.Printf("branch %s  good %s  bad %s  testing %s  (%d candidates left)\n",
		info.Branch, info.Good, info.Bad, info.Current, info.CandidatesLeft)
	return nil
}

func cmdBisectReset(args []string) error {
	fs := flag.NewFlagSet("bisect reset", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	// Open WITHOUT a pre-sync: after convergence the session is gone, so a sync
	// would snapshot the first-bad commit left in the folder into the working
	// change. reset only restores the folder; it never needs live edits captured.
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.BisectReset(); err != nil {
		return mapErr(err)
	}
	fmt.Fprintln(os.Stderr, "cairn: bisect reset; working folder restored")
	return nil
}
