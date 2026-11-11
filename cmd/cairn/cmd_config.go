package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
)

// errSilent makes main exit 1 without printing (git config --get on an unset key).
var errSilent = errors.New("")

// exitCodeError carries a child process's exit status (shell aliases).
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

var configKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*(\.[^\s=.][^\s=]*)*\.[A-Za-z][A-Za-z0-9-]*$`)

// normConfigKey validates a key's shape (section.name or section.sub.name) and
// lowercases the section and name like git (a subsection keeps its case).
func normConfigKey(key string) (string, error) {
	if key == "autosync" { // cairn's one sectionless key
		return key, nil
	}
	if !strings.Contains(key, ".") {
		return "", fmt.Errorf("key does not contain a section: %s", key)
	}
	if !configKeyRe.MatchString(key) {
		return "", fmt.Errorf("invalid key: %s", key)
	}
	first := strings.Index(key, ".")
	last := strings.LastIndex(key, ".")
	return strings.ToLower(key[:first]) + key[first:last] + "." + strings.ToLower(key[last+1:]), nil
}

// validateConfigValue checks the keys cairn or git give a fixed vocabulary;
// every other well-shaped key is stored as given.
func validateConfigValue(key, value string) error {
	isBool := func(v string) bool {
		switch strings.ToLower(v) {
		case "true", "false", "yes", "no", "on", "off", "1", "0", "":
			return true
		}
		return false
	}
	switch key {
	case "autosync":
		if !isBool(value) {
			return fmt.Errorf("%s: %q is not a boolean (true/false)", key, value)
		}
	case "pull.rebase":
		if !isBool(value) && value != "merges" && value != "interactive" {
			return fmt.Errorf("%s: %q is not true, false, merges or interactive", key, value)
		}
	case "push.default":
		switch value {
		case "nothing", "current", "upstream", "tracking", "simple", "matching":
		default:
			return fmt.Errorf("%s: %q is not one of nothing, current, upstream, simple, matching", key, value)
		}
	case "color.ui":
		if !isBool(value) && value != "always" && value != "never" && value != "auto" {
			return fmt.Errorf("%s: %q is not a boolean or always/never/auto", key, value)
		}
	case "init.defaultbranch":
		if err := change.ValidRefName(value); err != nil {
			return fmt.Errorf("%s: %q: %w", key, value, err)
		}
	}
	return nil
}

// cmdConfig is git's `config`: `config <key>` prints (or, with a value, sets);
// --list/-l lists the merged global+local config (--show-origin names the
// file); --get prints a value (exit 1 when unset); --unset removes one;
// --global / --local pick the scope. Any well-shaped section.name key is
// stored — alias.*, core.editor, user.*, pull.rebase… — with the few keys that
// have a fixed vocabulary validated.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	global := fs.Bool("global", false, "read/write the user-level (global) config instead of this repo's")
	local := fs.Bool("local", false, "read/write this repo's config (the default)")
	list := fs.Bool("list", false, "list the merged config")
	fs.BoolVar(list, "l", false, "list the merged config")
	get := fs.Bool("get", false, "print a value; exit 1 when unset")
	unset := fs.Bool("unset", false, "remove a key")
	origin := fs.Bool("show-origin", false, "with --list/--get, name the file each value comes from")
	fs.Bool("system", false, "")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.Lookup("system").Value.String() == "true" {
		return errors.New("--system is not supported: cairn has a user-level (--global) and a repo-level (--local) config")
	}
	if *global && *local {
		return errors.New("--global and --local are mutually exclusive")
	}
	gpath, _ := userconfig.Path()

	// Repo access is lazy: --global needs no repo.
	var rcfg func() (map[string]string, error)
	var rset func(k, v string) error
	var runset func(k string) (bool, error)
	var rpath string
	openR := func() (func(), error) {
		r, err := openRepo(*repo, *author)
		if err != nil {
			return nil, err
		}
		rpath = filepath.Join(r.Root(), ".cairn", "cairn.db")
		rcfg = r.AllConfig
		rset = r.SetConfig
		runset = r.UnsetConfig
		return func() { r.Close() }, nil
	}
	needRepo := !*global
	if needRepo {
		closeFn, err := openR()
		if err != nil {
			if *list || !*local {
				// Outside a repo `config -l`/get still reads the global file.
				if *list {
					*global = true
					needRepo = false
				} else if fs.NArg() <= 1 && !*unset {
					*global = true
					needRepo = false
				} else {
					return mapErr(err)
				}
			} else {
				return mapErr(err)
			}
		} else {
			defer closeFn()
		}
	}

	if *list {
		type kv struct{ k, v, src string }
		var rows []kv
		add := func(m map[string]string, src string) {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				rows = append(rows, kv{k, m[k], src})
			}
		}
		if !*local || *global {
			g, err := userconfig.All()
			if err != nil {
				return mapErr(err)
			}
			add(g, gpath)
		}
		if needRepo && !*global {
			l, err := rcfg()
			if err != nil {
				return mapErr(err)
			}
			add(l, rpath)
		}
		for _, row := range rows {
			if *origin {
				fmt.Printf("file:%s\t%s=%s\n", row.src, row.k, row.v)
			} else {
				fmt.Printf("%s=%s\n", row.k, row.v)
			}
		}
		return nil
	}

	if fs.NArg() < 1 {
		return errors.New("usage: cairn config [--global|--local] [--list] [--get] [--unset] <key> [value]")
	}
	key, err := normConfigKey(fs.Arg(0))
	if err != nil {
		return err
	}

	if *unset {
		var ok bool
		if *global {
			ok, err = userconfig.Unset(key)
		} else {
			ok, err = runset(key)
		}
		if err != nil {
			return mapErr(err)
		}
		if !ok {
			return errSilent // git: exit 5 for a missing key; 1 here
		}
		return nil
	}

	if fs.NArg() == 1 || *get {
		var value, src string
		set := false
		if !*global {
			m, err := rcfg()
			if err != nil {
				return mapErr(err)
			}
			if v, ok := m[key]; ok && (v != "" || *get) {
				value, set, src = v, true, rpath
			}
		}
		if !set && !*local {
			if v := userconfig.Get(key); v != "" {
				value, set, src = v, true, gpath
			}
		}
		if !set && *get {
			return errSilent
		}
		if *origin {
			fmt.Printf("file:%s\t%s\n", src, value)
		} else {
			fmt.Println(value)
		}
		return nil
	}

	value := fs.Arg(1)
	if err := validateConfigValue(key, value); err != nil {
		return err
	}
	if *global {
		if err := userconfig.Set(key, value); err != nil {
			return mapErr(err)
		}
		fmt.Fprintf(os.Stderr, "cairn: set --global %s=%s\n", key, value)
		return nil
	}
	if err := rset(key, value); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: set %s=%s\n", key, value)
	return nil
}

// ---- aliases ----

var aliasChain []string

// aliasValue looks up alias.<name> in the repo config (when run inside one),
// then the global config.
func aliasValue(name string, args []string) (string, bool) {
	exp, _, ok := aliasLookup(name, args)
	return exp, ok
}

// aliasStart is where alias lookup (and a shell alias) anchors: the repo
// --repo names, else the one you stand in.
func aliasStart(args []string) string {
	start := "."
	for i, a := range args {
		if a == "--repo" && i+1 < len(args) {
			start = args[i+1]
		} else if v, ok := strings.CutPrefix(a, "--repo="); ok {
			start = v
		}
	}
	return start
}

// aliasLookup returns the expansion and the repo root it was looked up in
// ("" outside any repo).
func aliasLookup(name string, args []string) (string, string, bool) {
	key := "alias." + strings.ToLower(name)
	root, rerr := discoverRepoRoot(aliasStart(args))
	if rerr == nil {
		if eng, err := change.Open(filepath.Join(root, ".cairn")); err == nil {
			v, ok, _ := eng.GetConfig(key)
			_ = eng.Close()
			if ok && v != "" {
				if strings.HasPrefix(v, "!") && !repoConfigTrusted(root) {
					fmt.Fprintf(os.Stderr, "cairn: ignoring shell alias %q from a repo catalogue not owned by you\n", name)
				} else {
					return v, root, true
				}
			}
		}
	} else {
		root = ""
	}
	if v := userconfig.Get(key); v != "" {
		return v, root, true
	}
	return "", "", false
}

// splitAliasWords splits an alias expansion like a shell would for the simple
// cases aliases use: whitespace-separated words, '...' and "..." quoting, and
// backslash escapes.
func splitAliasWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	in := false
	var quote rune
	esc := false
	for _, c := range s {
		switch {
		case esc:
			cur.WriteRune(c)
			esc = false
		case c == '\\' && quote != '\'':
			esc, in = true, true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(c)
			in = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in alias")
	}
	if in {
		words = append(words, cur.String())
	}
	return words, nil
}

// runAlias expands alias.<sub> and dispatches it. It is consulted only for a
// word that is not a built-in verb, so an alias can never shadow one (git's
// rule). A `!` expansion runs through the shell with the extra arguments
// appended; any other is a cairn command line.
func runAlias(sub string, rest []string) (handled bool, err error) {
	exp, aliasRoot, ok := aliasLookup(sub, rest)
	if !ok {
		return false, nil
	}
	for _, seen := range aliasChain {
		if seen == sub {
			return true, fmt.Errorf("alias loop detected: expansion of %q refers to itself (%s)", sub, strings.Join(append(aliasChain, sub), " -> "))
		}
	}
	if strings.HasPrefix(exp, "!") {
		cmd := exec.Command("sh", "-c", exp[1:]+` "$@"`, sub)
		cmd.Args = append(cmd.Args, rest...)
		if aliasRoot != "" {
			cmd.Dir = aliasRoot
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return true, exitCodeError{ee.ExitCode()}
			}
			return true, fmt.Errorf("alias %s: %w", sub, err)
		}
		return true, nil
	}
	words, err := splitAliasWords(exp)
	if err != nil {
		return true, fmt.Errorf("alias %s: %w", sub, err)
	}
	if len(words) == 0 {
		return true, fmt.Errorf("alias %s expands to nothing", sub)
	}
	aliasChain = append(aliasChain, sub)
	defer func() { aliasChain = aliasChain[:len(aliasChain)-1] }()
	return true, run(append(words, rest...))
}
