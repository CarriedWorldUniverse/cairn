package main

import (
	"regexp"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// --word-diff[=plain|color|none], --color-words[=<regex>]: git's word diff,
// applied to the hunks of a git-format patch. Changed words are wrapped
// ([-old-] {+new+}, or coloured), unchanged text is printed once, and the
// leading ' ' / '-' / '+' of the lines is gone.

type wordDiff struct {
	mode  string // "plain" | "color"
	regex *regexp.Regexp
	color bool // paint the headers too
}

type wordSpan struct{ begin, end int }

// words splits text into words: runs of non-space, or the regex matches.
func (w wordDiff) words(text string) []wordSpan {
	var out []wordSpan
	if w.regex != nil {
		for _, m := range w.regex.FindAllStringIndex(text, -1) {
			if m[1] > m[0] {
				out = append(out, wordSpan{m[0], m[1]})
			}
		}
		return out
	}
	isSpace := func(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') }
	for i := 0; i < len(text); {
		for i < len(text) && isSpace(text[i]) {
			i++
		}
		j := i
		for j < len(text) && !isSpace(text[j]) {
			j++
		}
		if j > i {
			out = append(out, wordSpan{i, j})
		}
		i = j
	}
	return out
}

// emit writes text with each line wrapped in the style's markers.
func (w wordDiff) emit(sb *strings.Builder, text, open, closeM string) {
	for text != "" {
		line, rest, hasNL := strings.Cut(text, "\n")
		if line != "" && (open != "" || closeM != "") {
			sb.WriteString(open + line + closeM)
		} else {
			sb.WriteString(line)
		}
		if hasNL {
			sb.WriteString("\n")
		}
		text = rest
	}
}

func (w wordDiff) styles() (oldOpen, oldClose, newOpen, newClose string) {
	oldOpen, oldClose, newOpen, newClose = "", "", "", ""
	if w.mode == "plain" {
		oldOpen, oldClose, newOpen, newClose = "[-", "-]", "{+", "+}"
	}
	if w.color {
		oldOpen, oldClose = "\x1b[31m"+oldOpen, oldClose+"\x1b[m"
		newOpen, newClose = "\x1b[32m"+newOpen, newClose+"\x1b[m"
	}
	return
}

// flush prints one block of removed and added text.
func (w wordDiff) flush(sb *strings.Builder, minus, plus string) {
	if minus == "" && plus == "" {
		return
	}
	oo, oc, no, nc := w.styles()
	if plus == "" {
		w.emit(sb, minus, oo, oc)
		return
	}
	mw, pw := w.words(minus), w.words(plus)
	ms, ps := make([]string, len(mw)), make([]string, len(pw))
	for i, s := range mw {
		ms[i] = minus[s.begin:s.end]
	}
	for i, s := range pw {
		ps[i] = plus[s.begin:s.end]
	}
	chM, chP := change.DiffWords(ms, ps)
	cur := 0 // how much of plus is already printed
	i, j := 0, 0
	for i < len(ms) || j < len(ps) {
		if i < len(ms) && j < len(ps) && !chM[i] && !chP[j] {
			i++
			j++
			continue
		}
		i0, j0 := i, j
		for i < len(ms) && chM[i] {
			i++
		}
		for j < len(ps) && chP[j] {
			j++
		}
		var pb, pe int
		if j == j0 { // only removal
			// the removal sits right after the previous word that stayed
			if j0 > 0 {
				pb = pw[j0-1].end
			}
			pe = pb
		} else {
			pb, pe = pw[j0].begin, pw[j-1].end
		}
		if cur < pb {
			w.emit(sb, plus[cur:pb], "", "")
		}
		if i > i0 {
			w.emit(sb, minus[mw[i0].begin:mw[i-1].end], oo, oc)
		}
		if j > j0 {
			w.emit(sb, plus[pb:pe], no, nc)
		}
		cur = max(cur, pe)
	}
	if cur < len(plus) {
		w.emit(sb, plus[cur:], "", "")
	}
}

// body rewrites a patch's hunks (the text after the "+++" line).
func (w wordDiff) body(s string) string {
	var sb strings.Builder
	var minus, plus strings.Builder
	flush := func() {
		w.flush(&sb, minus.String(), plus.String())
		minus.Reset()
		plus.Reset()
	}
	for _, ln := range strings.SplitAfter(s, "\n") {
		if ln == "" {
			continue
		}
		switch ln[0] {
		case '@':
			flush()
			if w.color {
				body := strings.TrimSuffix(ln, "\n")
				if end := strings.Index(body[2:], "@@"); end >= 0 {
					sb.WriteString(paint(true, cCyan, body[:end+4]))
					if tail, ok := strings.CutPrefix(body[end+4:], " "); ok {
						sb.WriteString(" \x1b[m" + tail + "\x1b[m")
					}
					sb.WriteString("\n")
					continue
				}
			}
			sb.WriteString(ln)
		case '-':
			minus.WriteString(ln[1:])
		case '+':
			plus.WriteString(ln[1:])
		case '\\':
			// "\ No newline at end of file": the text just ends there.
			flush()
		case ' ':
			flush()
			if text, ok := strings.CutSuffix(ln[1:], "\n"); ok && text != "" && w.color {
				sb.WriteString(text + "\x1b[m\n")
			} else {
				sb.WriteString(ln[1:])
			}
		default:
			flush()
			sb.WriteString(ln)
		}
	}
	flush()
	return sb.String()
}
