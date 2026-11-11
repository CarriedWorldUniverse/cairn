package main

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
)

// Git-style pager. When stdout is a terminal, the output of the listing and
// history commands is piped through a pager chosen like git does:
//
//	$CAIRN_PAGER > $GIT_PAGER > core.pager > $PAGER > less
//
// with LESS=FRX and LV=-c defaulted when unset. The global flags --no-pager/-P
// and --paginate/-p come before the subcommand. stdout that is not a terminal
// is never paged, --paginate or not.
//
// core.pager and pager.<cmd> are read from the repo config first, then the
// user-level config, as git does.

// pagerCommands are the subcommands whose output is paged.
var pagerCommands = map[string]bool{
	"log": true, "show": true, "diff": true, "grep": true, "blame": true,
	"shortlog": true, "ls-files": true, "ls-tree": true, "rev-list": true,
	"oplog": true, "tree": true, "ls": true,
	"branch": true, "tag": true, "reflog": true, "help": true,
}

// pagerActive is true while stdout is a pipe to a pager. Colour "auto" and
// decoration treat it as a terminal, as git does.
var pagerActive bool

type pagerMode int

const (
	pagerAuto  pagerMode = iota
	pagerNever           // --no-pager / -P
	pagerForce           // --paginate / -p
)

// pagerGlobals strips the leading global pager flags and reports the mode.
func pagerGlobals(args []string) ([]string, pagerMode) {
	mode := pagerAuto
	for len(args) > 0 {
		switch args[0] {
		case "--no-pager", "-P":
			mode = pagerNever
		case "--paginate", "-p":
			mode = pagerForce
		default:
			return args, mode
		}
		args = args[1:]
	}
	return args, mode
}

// pagerCommand picks the pager for sub, or "" for none. A set-but-empty
// environment variable, "cat" or pager.<sub>=false all mean no paging.
func pagerCommand(sub string, mode pagerMode) string {
	if mode == pagerNever {
		return ""
	}
	cmd, custom := "", ""
	if mode != pagerForce {
		if !pagerCommands[sub] {
			return ""
		}
	}
	if v := pagerConfig("pager." + sub); v != "" {
		switch strings.ToLower(v) {
		case "false", "no", "off", "0":
			if mode != pagerForce {
				return ""
			}
		case "true", "yes", "on", "1":
		default:
			custom = v
		}
	}
	if custom != "" {
		cmd = custom
	} else if v, ok := os.LookupEnv("CAIRN_PAGER"); ok {
		cmd = v
	} else if v, ok := os.LookupEnv("GIT_PAGER"); ok {
		cmd = v
	} else if v := pagerConfig("core.pager"); v != "" {
		cmd = v
	} else if v, ok := os.LookupEnv("PAGER"); ok {
		cmd = v
	} else {
		cmd = "less"
	}
	if strings.TrimSpace(cmd) == "" || strings.TrimSpace(cmd) == "cat" {
		return ""
	}
	return cmd
}

// pagerConfig reads a config key for the pager; startPager points it at the
// repo the command runs in. Tests and non-repo use fall back to user config.
var pagerConfig = userconfig.Get

// repoThenUserConfig looks key up in the repo --repo names (or the one the
// cwd is in), then in the user-level config.
func repoThenUserConfig(args []string) func(string) string {
	return func(key string) string {
		if root, err := discoverRepoRoot(aliasStart(args)); err == nil {
			if eng, err := change.Open(filepath.Join(root, ".cairn")); err == nil {
				v, ok, _ := eng.GetConfig(key)
				_ = eng.Close()
				if ok && v != "" {
					return v
				}
			}
		}
		return userconfig.Get(key)
	}
}

// startPager redirects os.Stdout into the pager when args name a paged command
// and tty says stdout is a terminal. The returned stop flushes, waits for the
// pager to exit and restores stdout; it is always safe to call.
func startPager(args []string, mode pagerMode, tty bool) (stop func()) {
	noop := func() {}
	if !tty || len(args) == 0 || runtime.GOOS == "windows" {
		return noop
	}
	pagerConfig = repoThenUserConfig(args)
	cmdline := pagerCommand(args[0], mode)
	if cmdline == "" {
		return noop
	}
	// A simple "prog args" pager that cannot be executed (missing, or not
	// executable) is skipped up front, as git falls back to plain output.
	if !strings.ContainsAny(cmdline, "|&;<>()$`\\\"'*?[#~=%\n") {
		if f := strings.Fields(cmdline); len(f) > 0 {
			if _, lerr := exec.LookPath(f[0]); lerr != nil {
				return noop
			}
		}
	}
	// cairn writes into pr/pw; a forwarder goroutine relays it to the pager.
	// Routing through the forwarder means a pager that dies (cannot exec, exits
	// non-zero at once) can never break cairn's own stdout writes: if the pager
	// fails, the output is delivered directly instead of being lost.
	pr, pw, err := os.Pipe()
	if err != nil {
		return noop
	}
	real := os.Stdout
	c := exec.Command("sh", "-c", cmdline)
	c.Stdout = real
	c.Stderr = os.Stderr
	c.Env = os.Environ()
	if _, ok := os.LookupEnv("LESS"); !ok {
		c.Env = append(c.Env, "LESS=FRX")
	}
	if _, ok := os.LookupEnv("LV"); !ok {
		c.Env = append(c.Env, "LV=-c")
	}
	in, err := c.StdinPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return noop
	}
	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		return noop
	}
	waited := make(chan int, 1)
	go func() {
		_ = c.Wait()
		waited <- c.ProcessState.ExitCode()
	}()
	fwdDone := make(chan struct{})
	go func() {
		defer close(fwdDone)
		defer in.Close()
		pagerGone, direct := false, false
		buf := make([]byte, 32<<10)
		for {
			n, rerr := pr.Read(buf)
			if n > 0 && !pagerGone {
				if _, werr := in.Write(buf[:n]); werr != nil {
					pagerGone = true
					// The pager is gone. Quitting it early (exit 0, e.g. less's q)
					// silently drops the rest, as git does; a pager that FAILED
					// must not swallow the output.
					direct = <-waited != 0
					waited <- 1 // keep the value for stop (never read as success)
					if direct {
						_, _ = real.Write(buf[:n])
					}
				}
			} else if n > 0 && direct {
				_, _ = real.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	os.Stdout = pw
	pagerActive = true
	// The pager owns ^C (less handles it itself); we must not die under it.
	signal.Ignore(os.Interrupt)
	return func() {
		pw.Close()
		<-fwdDone
		pr.Close()
		<-waited
		os.Stdout = real
		pagerActive = false
		signal.Reset(os.Interrupt)
	}
}
