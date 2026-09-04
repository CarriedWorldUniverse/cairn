package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/credstore"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// defaultAuthor resolves the commit author from the environment.
// defaultAuthor is the explicit identity override from the environment only.
// cairn's identity otherwise comes from its own config (repo → global), set via
// `cairn setup` / `cairn config --global` — never silently from git. Empty here
// means "not explicitly overridden" and lets the config layers resolve it.
func defaultAuthor() string { return os.Getenv("CAIRN_AUTHOR") }

// gitConfigValue returns `git config --get <key>` (respecting local + global +
// system scopes), or "" when git is unavailable or the key is unset. It is used
// only to PRE-FILL suggestions during first-use `cairn setup`, never as a silent
// default — cairn owns its identity once you confirm it into the global config.
func gitConfigValue(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isInteractive reports whether stdin is a terminal (so cairn may prompt). In a
// script, agent, or CI run it is not, and cairn errors with setup guidance
// instead of blocking on a prompt.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// promptLine prints "label [def]: " to stderr and reads a line from stdin;
// an empty reply keeps def.
func promptLine(label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	line, _ := stdinReader.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

// collectIdentity prompts for a name and email (pre-filled from cur… and your git
// config) and stores them in the GLOBAL cairn config. No repo required, so
// `cairn setup` works anywhere. Returns the chosen values.
func collectIdentity(curName, curEmail string) (string, string, error) {
	name := promptLine("Your name", firstNonEmptyStr(curName, gitConfigValue("user.name")))
	email := promptLine("Your email", firstNonEmptyStr(curEmail, gitConfigValue("user.email")))
	if name == "" || email == "" {
		return "", "", errors.New("name and email are required")
	}
	if err := userconfig.Set("user.name", name); err != nil {
		return "", "", err
	}
	if err := userconfig.Set("user.email", email); err != nil {
		return "", "", err
	}
	fmt.Fprintf(os.Stderr, "cairn: saved identity %s <%s> to the global config\n", name, email)
	return name, email, nil
}

// ensureIdentity settles the author identity before a commit lands, best-effort
// and never blocking (the CLI+agent-first contract: a script/agent must never
// hang or hard-fail on a prompt). Precedence: cairn's own config (repo→global→
// env, already resolved into r.Identity) wins; any gap is filled from git config
// so non-interactive commits still get real attribution; and only on a real
// terminal with nothing configured do we run gh-style first-use setup (saved
// globally, asked once). Whatever is still missing is covered by the engine's
// commit-time placeholder — cairn prefers its own identity but never refuses to
// commit for lack of one.
func ensureIdentity(r *worktree.Repo) {
	name, email := r.Identity()
	if name != "" && email != "" {
		return
	}
	// Fill gaps from the git identity configured in this environment.
	name = firstNonEmptyStr(name, gitConfigValue("user.name"))
	email = firstNonEmptyStr(email, gitConfigValue("user.email"))
	if (name == "" || email == "") && isInteractive() {
		fmt.Fprintln(os.Stderr, "cairn: no identity set — let's set it up (saved globally, asked once).")
		if n, e, err := collectIdentity(name, email); err == nil {
			name, email = n, e
		}
	}
	if name != "" || email != "" {
		r.SetIdentity(name, email)
	}
}

func cmdSetup(args []string) error {
	fmt.Fprintln(os.Stderr, "cairn setup — your commit identity (stored globally for all repos).")
	_, _, err := collectIdentity(userconfig.Get("user.name"), userconfig.Get("user.email"))
	return err
}

// cmdLogin stores an access token for a host in the user-level credential store
// (0600, outside every repo). The token is read from stdin when piped
// (agent-friendly: `echo $TOK | cairn login github.com`), else prompted on a TTY.
func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn login <host>   (token from stdin: echo $TOK | cairn login github.com)")
	}
	host := fs.Arg(0)
	var token string
	if fi, _ := os.Stdin.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		b, err := io.ReadAll(os.Stdin) // piped
		if err != nil {
			return fmt.Errorf("login: read token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	} else {
		fmt.Fprintf(os.Stderr, "Access token for %s: ", host)
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			token = strings.TrimSpace(sc.Text())
		}
	}
	if token == "" {
		return errors.New("login: empty token")
	}
	if err := credstore.Set(host, token); err != nil {
		return err
	}
	p, _ := credstore.Path()
	fmt.Printf("saved credential for %s (%s)\n", host, p)
	return nil
}

// cmdLogout removes a host's stored credential.
func cmdLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn logout <host>")
	}
	host := fs.Arg(0)
	if err := credstore.Delete(host); err != nil {
		return err
	}
	fmt.Printf("removed credential for %s\n", host)
	return nil
}

// cmdAuth lists the hosts that have a stored credential (never the tokens) plus
// any auth-related env vars that would take precedence.
func cmdAuth(args []string) error {
	hosts := credstore.Hosts()
	if len(hosts) == 0 {
		fmt.Println("no stored credentials (use: cairn login <host>)")
	} else {
		p, _ := credstore.Path()
		fmt.Printf("stored credentials (%s):\n", p)
		for _, h := range hosts {
			fmt.Printf("  %s\n", h)
		}
	}
	for _, k := range []string{"CAIRN_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN"} {
		if os.Getenv(k) != "" {
			fmt.Printf("env %s is set (takes precedence over the store)\n", k)
		}
	}
	return nil
}

// cmdConfig gets or sets a config value. With one arg it prints the value (an
// empty line when unset); with two args it stores the value.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	global := fs.Bool("global", false, "read/write the user-level (global) config instead of this repo's")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn config [--global] <key> [value]")
	}
	key := fs.Arg(0)
	if *global {
		// The global config is user-level and needs no repo.
		if fs.NArg() == 1 {
			fmt.Println(userconfig.Get(key))
			return nil
		}
		value := fs.Arg(1)
		if err := userconfig.Set(key, value); err != nil {
			return mapErr(err)
		}
		fmt.Fprintf(os.Stderr, "cairn: set --global %s=%s\n", key, value)
		return nil
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if fs.NArg() == 1 {
		value, set, err := r.GetConfig(key)
		if err != nil {
			return mapErr(err)
		}
		// Mirror identity resolution: an unset repo key falls through to the
		// global config, so `cairn config user.email` shows what a commit will
		// actually use (repo override, else your global identity).
		if !set || value == "" {
			value = userconfig.Get(key)
		}
		fmt.Println(value)
		return nil
	}
	value := fs.Arg(1)
	if err := r.SetConfig(key, value); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: set %s=%s\n", key, value)
	return nil
}
