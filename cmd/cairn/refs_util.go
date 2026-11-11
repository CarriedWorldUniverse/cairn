package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Helpers shared by the ref verbs (branch, tag, remote, reflog): git-style
// glob matching, short-cluster expansion, and the --format mini-language.

// globToRegexp compiles a fnmatch-style pattern (`*`, `?`, `[...]`, no
// path-separator special-casing — like `git branch --list` / `git tag -l`).
func globToRegexp(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := strings.IndexByte(pat[i+1:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := pat[i+1 : i+1+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + class + "]")
			i += j + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// matchAnyGlob reports whether name matches any pattern (true with none).
func matchAnyGlob(patterns []string, name string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, p := range patterns {
		re, err := globToRegexp(p)
		if err != nil {
			return false, fmt.Errorf("bad pattern %q: %w", p, err)
		}
		if re.MatchString(name) {
			return true, nil
		}
	}
	return false, nil
}

// expandShortCluster splits a combined run of boolean short flags (-vva,
// -dr) into separate flags so the flag package sees them; letters outside
// letters are left alone (a value-taking flag keeps its attached value).
func expandShortCluster(args []string, letters string) []string {
	var out []string
	stop := false
	for _, a := range args {
		if stop || a == "--" || len(a) < 3 || a[0] != '-' || a[1] == '-' {
			if a == "--" {
				stop = true
			}
			out = append(out, a)
			continue
		}
		all := true
		for _, c := range a[1:] {
			if !strings.ContainsRune(letters, c) {
				all = false
				break
			}
		}
		if !all {
			out = append(out, a)
			continue
		}
		for _, c := range a[1:] {
			out = append(out, "-"+string(c))
		}
	}
	return out
}

// takeOptionalRev removes every `--name [rev]` / `--name=rev` from args and
// returns the rev given (HEAD when the flag stands alone) for each occurrence.
func takeOptionalRev(args []string, name string) (rest []string, revs []string, seen bool) {
	flag := "--" + name
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest = append(rest, args[i:]...)
			return rest, revs, seen
		case a == flag:
			seen = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				revs = append(revs, args[i+1])
				i++
			} else {
				revs = append(revs, "HEAD")
			}
		case strings.HasPrefix(a, flag+"="):
			seen = true
			revs = append(revs, strings.TrimPrefix(a, flag+"="))
		default:
			rest = append(rest, a)
		}
	}
	return rest, revs, seen
}

// expandFormat expands %(field) / %(field:modifier) in a --format string via
// get; an unknown field is an error, like git's "unknown field name".
func expandFormat(format string, get func(field, mod string) (string, bool)) (string, error) {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) {
			if format[i+1] == '%' {
				b.WriteByte('%')
				i++
				continue
			}
			if format[i+1] == '(' {
				j := strings.IndexByte(format[i:], ')')
				if j < 0 {
					return "", fmt.Errorf("format: unterminated %%( in %q", format)
				}
				field := format[i+2 : i+j]
				mod := ""
				if k := strings.IndexByte(field, ':'); k >= 0 {
					field, mod = field[:k], field[k+1:]
				}
				v, ok := get(field, mod)
				if !ok {
					return "", fmt.Errorf("format: unknown field name: %s", field)
				}
				b.WriteString(v)
				i += j
				continue
			}
		}
		b.WriteByte(format[i])
	}
	return b.String(), nil
}

func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
