package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Second batch of git-parity checks (blame formats, decorate, describe,
// plumbing verbs): each command line is run through git and cairn and the
// outputs must be identical.
func TestE2E_Inspect2MatchesGit(t *testing.T) {
	gitDir, lineDir := parityReposWith(t, func(dir string) {
		// an annotated tag, to tell describe's defaults apart
		mustGit(t, dir, "tag", "-a", "-m", "annotated", "v2.0")
		// the clone records what it fetched as remote-tracking refs
		mustGit(t, dir, "update-ref", "refs/remotes/origin/main", "main")
		mustGit(t, dir, "update-ref", "refs/remotes/origin/feature", "feature")
	})
	cases := []string{
		// blame
		"blame src/a.txt",
		"blame -L 2,4 src/a.txt",
		"blame -L 2,+2 -L 9,10 src/a.txt",
		"blame -e src/a.txt",
		"blame -s src/a.txt",
		"blame -l src/a.txt",
		"blame --abbrev=4 src/a.txt",
		"blame -n src/a.txt",
		"blame -f src/a.txt",
		"blame -t src/a.txt",
		"blame --porcelain src/a.txt",
		"blame --line-porcelain src/a.txt",
		"blame --porcelain src/lib/lib.go",
		"blame docs/README.md",
		"blame --porcelain docs/README.md",
		"blame nonl.txt",
		"blame -w src/a.txt",
		"blame HEAD~3 -- src/a.txt",
		"blame -C -M src/a.txt",
		// describe: annotated tags only unless --tags
		"describe",
		"describe HEAD~1",
		"describe --tags HEAD~1",
		"describe --tags v1.0.0",
		"describe --always HEAD~1",
		// -m: one diff per merge parent
		"show -m --no-decorate --stat --no-renames HEAD~1",
		"show -m --no-decorate -p --no-renames HEAD~1",
		"show -m --oneline --no-decorate --name-status --no-renames HEAD~1",
		"log -m --oneline --no-decorate --stat --no-renames",
		"log -m --no-decorate --name-only --no-renames -3",
		"log -m --abbrev-commit --no-decorate --stat --no-renames -3",
		"log --first-parent -p --no-decorate --no-renames --oneline",
		// a merge without -m: combined diff (empty when clean); stats are first-parent
		"show --no-decorate --no-renames HEAD~1",
		"show --no-decorate --no-renames --stat HEAD~1",
		"show --no-decorate --no-renames -p --stat HEAD~1",
		"show --no-decorate --no-renames --numstat HEAD~1",
		"show --no-decorate --no-renames --shortstat HEAD~1",
		"show --no-decorate --no-renames --name-status HEAD~1",
		"show --no-decorate -c HEAD~1",
		"show --no-decorate --cc HEAD~1",
		// rev-list formats
		"rev-list --oneline HEAD",
		"rev-list --oneline --abbrev=10 -3 HEAD",
		"rev-list --pretty=oneline HEAD",
		"rev-list --pretty HEAD",
		"rev-list --pretty=short -3 HEAD",
		"rev-list --pretty=raw -1 HEAD",
		"rev-list --pretty=format:%s%n%b HEAD",
		"rev-list --format=%h:%s HEAD",
		"rev-list --abbrev-commit --pretty=format:%s -2 HEAD",
		"rev-list --pretty=fuller -2 HEAD",
		// --no-abbrev, --pretty=reference
		"log --oneline --no-abbrev",
		"log --no-abbrev --format=%h|%p|%t -3",
		"log --no-abbrev -2 --no-decorate",
		"log --pretty=reference",
		"log --pretty=reference --date=iso -2",
		"show -s --pretty=reference HEAD~2",
		"rev-list --pretty=reference HEAD",
		// --dirstat
		"diff --dirstat HEAD~6 HEAD --no-renames",
		"diff --dirstat=lines HEAD~6 HEAD --no-renames",
		"diff --dirstat=files,cumulative HEAD~6 HEAD --no-renames",
		"diff --dirstat=0 HEAD~6 HEAD --no-renames",
		"diff --dirstat=changes,20 HEAD~4 HEAD --no-renames",
		"diff --dirstat --stat HEAD~6 HEAD --no-renames",
		"show --dirstat --oneline HEAD~3 --no-renames",
		"log --dirstat --oneline --no-renames",
		"diff --dirstat --summary HEAD~6 HEAD --no-renames",
		// plumbing
		"merge-base main feature",
		"merge-base --all main feature",
		"merge-base HEAD~1 HEAD",
		"merge-base --is-ancestor v1.0.0 HEAD",
		"merge-base --is-ancestor HEAD v1.0.0",
		"merge-base --is-ancestor feature main",
		"symbolic-ref HEAD",
		"symbolic-ref --short HEAD",
		"symbolic-ref -q HEAD",
		"for-each-ref",
		"for-each-ref refs/heads",
		"for-each-ref refs/tags",
		"for-each-ref refs/heads/f*",
		"for-each-ref --format=%(refname)",
		"for-each-ref --format=%(refname:short)|%(objectname:short)|%(objecttype)|%(subject) refs/heads refs/tags",
		"for-each-ref --format=%(refname)%20%(*objectname)%20%(*objecttype) refs/tags",
		"for-each-ref --sort=-refname --format=%(refname)",
		"for-each-ref --sort=-committerdate --format=%(refname)",
		"for-each-ref --sort=objecttype --sort=refname --format=%(objecttype)%20%(refname)",
		"for-each-ref --count=2 --format=%(refname)",
		"for-each-ref --format=%(authorname)|%(authoremail)|%(authordate)|%(committerdate:short)|%(creatordate:iso) refs/heads",
		"for-each-ref --format=%(taggername)|%(taggeremail)|%(taggerdate:unix)|%(contents)|%(body) refs/tags/v2.0",
		"for-each-ref --format=%(HEAD)%20%(refname:short) refs/heads",
		"for-each-ref --format=%(refname:lstrip=2)|%(refname:rstrip=1) refs/heads",
		"name-rev HEAD",
		"name-rev HEAD~1 HEAD~2 HEAD~3 HEAD~4",
		"name-rev --tags HEAD~3 HEAD",
		"name-rev --tags --name-only HEAD~3 HEAD",
		"name-rev --name-only v1.0.0",
		"name-rev v2.0",
		"name-rev --refs=refs/heads/f* main~1^2",
		"name-rev --no-undefined --always HEAD~1",
		"name-rev feature",
		// describe --contains
		"describe --contains HEAD",
		"describe --contains HEAD~1",
		"describe --contains HEAD~3",
		"describe --contains HEAD~4",
		"describe --contains v1.0.0",
		"describe --contains --always HEAD~3",
		"describe --contains feature",
		"describe --contains --match=v1* HEAD~3",
		// grep -h suppresses file names (it is not help here)
		"grep -h two",
		"grep -h -n e -- src",
		// decorations: tags (lightweight and annotated), full names
		"log --oneline --decorate",
		"log --oneline --decorate=full",
		"log --oneline --decorate=no",
		"log --oneline --decorate=short --all",
		"log --format=%D --decorate=full",
		"log --format=%d",
		"show -s --oneline --decorate=full HEAD",
	}
	for _, c := range cases {
		compareWithGit(t, gitDir, lineDir, c)
	}
}

func compareWithGit(t *testing.T, gitDir, lineDir, c string) {
	t.Helper()
	args := strings.Fields(c)
	want, wantCode := gitRun(t, gitDir, args...)
	got, gotCode := cairnRun(t, lineDir, args...)
	if got != want {
		t.Errorf("%s:\n--- cairn\n%s--- git\n%s", c, got, want)
	}
	if (gotCode == 0) != (wantCode == 0) {
		t.Errorf("%s: exit %d, git exits %d", c, gotCode, wantCode)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Ann Author", "GIT_AUTHOR_EMAIL=ann@example.com", "GIT_COMMITTER_NAME=Ann Author", "GIT_COMMITTER_EMAIL=ann@example.com",
		"GIT_COMMITTER_DATE=2026-01-08T09:00:00+0000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestE2E_ReflogRevisions(t *testing.T) {
	root := refsRepo(t)
	refsCommit(t, root, "main", "b.txt", "b\n", "second")
	refsCommit(t, root, "main", "c.txt", "c\n", "third")
	rp := func(rev string) string {
		return strings.TrimSpace(refsOut(t, "rev-parse", "--repo", root, rev))
	}
	for i, want := range []string{"HEAD", "HEAD~1", "HEAD~2"} {
		w := rp(want)
		if got := rp("HEAD@{" + strconv.Itoa(i) + "}"); got != w {
			t.Errorf("HEAD@{%d} = %s, want %s", i, got, w)
		}
		if got := rp("main@{" + strconv.Itoa(i) + "}"); got != w {
			t.Errorf("main@{%d} = %s, want %s", i, got, w)
		}
	}
	if got, want := rp("HEAD@{1}~1"), rp("HEAD~2"); got != want {
		t.Errorf("HEAD@{1}~1 = %s, want %s", got, want)
	}
	if _, _, err := runOutErr(t, "rev-parse", "--repo", root, "HEAD@{99}"); err == nil {
		t.Errorf("HEAD@{99} resolved")
	}
	// the reflog's first entry is the current commit
	out := refsOut(t, "reflog", "--repo", root)
	if !strings.HasPrefix(out, rp("HEAD")[:7]+" HEAD@{0}: commit") {
		t.Errorf("reflog first line does not name HEAD:\n%s", out)
	}
}

func TestGitHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 bytes", 1024: "1024 bytes", 12288: "12.00 KiB", 1536 * 1024: "1.50 MiB", 5 << 30: "5.00 GiB"} {
		if got := gitHumanBytes(n); got != want {
			t.Errorf("gitHumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	root := refsRepo(t)
	out := refsOut(t, "count-objects", "--repo", root, "-H")
	if !regexp.MustCompile(`^\d+ objects, (\d+ bytes|\d+\.\d\d (KiB|MiB))\n$`).MatchString(out) {
		t.Errorf("count-objects -H = %q", out)
	}
	if out := refsOut(t, "count-objects", "--repo", root); !strings.Contains(out, " kilobytes\n") {
		t.Errorf("count-objects = %q", out)
	}
}

func TestE2E_StatusPorcelainV2Branch(t *testing.T) {
	root := refsRepo(t)
	head := strings.TrimSpace(refsOut(t, "rev-parse", "--repo", root, "HEAD"))
	out := refsOut(t, "status", "--repo", root, "--porcelain=v2", "-b")
	if !strings.HasPrefix(out, "# branch.oid "+head+"\n# branch.head main\n") {
		t.Errorf("status --porcelain=v2 -b:\n%s", out)
	}
	if out := refsOut(t, "status", "--repo", root, "--porcelain=v2"); out != "" {
		t.Errorf("clean status --porcelain=v2 = %q", out)
	}
}

// A history with word-level edits, for --word-diff.
func TestE2E_WordDiffMatchesGit(t *testing.T) {
	gitDir, lineDir := parityReposWith(t, func(dir string) {
		write := func(s string) {
			if err := os.WriteFile(filepath.Join(dir, "w.txt"), []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("The quick brown fox jumps over the lazy dog\nsecond line stays\nremove this entire line\nfourth line has words to change here\nalpha beta gamma delta\nx y\ntail\n")
		mustGit(t, dir, "add", "-A")
		mustGit(t, dir, "commit", "-q", "-m", "words one")
		write("The quick red fox jumps over the lazy cat\nsecond line stays\nfourth line has some words to modify here\na brand new line\nalpha gamma delta\nx new y\ntail")
		mustGit(t, dir, "add", "-A")
		mustGit(t, dir, "commit", "-q", "-m", "words two")
	})
	for _, c := range []string{
		"diff --word-diff HEAD~1 HEAD -- w.txt",
		"diff --word-diff=plain HEAD~1 HEAD -- w.txt",
		"diff --word-diff=color HEAD~1 HEAD -- w.txt",
		"diff --color-words HEAD~1 HEAD -- w.txt",
		"diff --word-diff --color=always HEAD~1 HEAD -- w.txt",
		"diff --word-diff-regex=[a-z]+ --word-diff HEAD~1 HEAD -- w.txt",
		"diff --word-diff=none HEAD~1 HEAD -- w.txt",
		"show --word-diff --no-decorate HEAD",
		"log --word-diff -p --oneline -2 -- w.txt",
		"diff --word-diff HEAD~2 HEAD~1 --no-renames",
		"diff --color-moved HEAD~1 HEAD -- w.txt",
		"diff --color-moved=zebra --color-moved-ws=ignore-all-space HEAD~1 HEAD -- w.txt",
	} {
		compareWithGit(t, gitDir, lineDir, c)
	}
}

func TestE2E_HashObject(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	f := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(f, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"hash-object " + f, "hash-object -t blob " + f, "hash-object -t commit " + f} {
		compareWithGit(t, gitDir, lineDir, c)
	}
	// -w stores it
	sha, _ := cairnRun(t, lineDir, "hash-object", "-w", f)
	if got, _ := cairnRun(t, lineDir, "cat-file", "-p", strings.TrimSpace(sha)); got != "hello\nworld\n" {
		t.Errorf("hash-object -w then cat-file -p = %q", got)
	}
	// --stdin
	old := os.Stdin
	rp, wp, _ := os.Pipe()
	_, _ = wp.WriteString("hello\nworld\n")
	_ = wp.Close()
	os.Stdin = rp
	defer func() { os.Stdin = old }()
	if got, _ := cairnRun(t, lineDir, "hash-object", "--stdin"); got != sha {
		t.Errorf("hash-object --stdin = %q, want %q", got, sha)
	}
	_ = gitDir
}

func TestE2E_PlumbingRefusals(t *testing.T) {
	_, lineDir := parityRepos(t)
	for _, c := range [][]string{
		{"merge-base", "--fork-point", "main", "feature"},
		{"symbolic-ref", "HEAD", "refs/heads/feature"},
		{"show-branch", "main", "feature"},
	} {
		if _, code := cairnRun(t, lineDir, c...); code == 0 {
			t.Errorf("%v succeeded", c)
		}
	}
}

func TestE2E_LogLMatchesGit(t *testing.T) {
	gitDir, lineDir := parityReposWith(t, func(dir string) {
		write := func(s string) {
			if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		commit := func(msg, date string) {
			mustGit(t, dir, "add", "-A")
			cmd := exec.Command("git", "commit", "-q", "-m", msg)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
				"GIT_AUTHOR_NAME=Ann Author", "GIT_AUTHOR_EMAIL=ann@example.com", "GIT_COMMITTER_NAME=Ann Author", "GIT_COMMITTER_EMAIL=ann@example.com",
				"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("commit: %v\n%s", err, out)
			}
		}
		write("package f\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\treturn 2\n}\n\nfunc Three() int {\n\treturn 3\n}\n")
		commit("f: create", "2026-02-01T10:00:00+0000")
		write("package f\n\nimport \"fmt\"\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\treturn 2\n}\n\nfunc Three() int {\n\treturn 3\n}\n")
		commit("f: add import (shifts everything)", "2026-02-02T10:00:00+0000")
		write("package f\n\nimport \"fmt\"\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\tfmt.Println(\"two\")\n\treturn 22\n}\n\nfunc Three() int {\n\treturn 3\n}\n")
		commit("f: change Two", "2026-02-03T10:00:00+0000")
		write("package f\n\nimport \"fmt\"\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\tfmt.Println(\"two\")\n\treturn 22\n}\n\nfunc Three() int {\n\treturn 33\n}\n\nfunc Four() int {\n\treturn 4\n}\n")
		commit("f: change Three, add Four", "2026-02-04T10:00:00+0000")
		write("package f\n\nimport \"fmt\"\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\tfmt.Println(\"two!\")\n\treturn 22\n}\n\n// Three counts.\nfunc Three() int {\n\treturn 33\n}\n\nfunc Four() int {\n\treturn 4\n}\n")
		commit("f: doc Three, tweak Two", "2026-02-05T10:00:00+0000")
	})
	for _, c := range []string{
		"log -L 5,7:f.go",
		"log -L 9,13:f.go --no-decorate",
		"log -L5,+3:f.go --oneline",
		"log -L 1,3:f.go",
		"log -L 15,17:f.go --oneline",
		"log -L 1,$:f.go --oneline",
		"log -L :Two:f.go --oneline",
		"log -L :Three:f.go",
		"log -L :One:f.go --oneline",
		"log -L /func Two/,/^}/:f.go --oneline",
		"log -L 9,13:f.go -s --oneline",
		"log -L 9,13:f.go -n 2 --oneline",
		"log -L 9,13:f.go --format=%h|%s",
		"log -L 2,4:src/a.txt",
		"log -L 9,11:src/a.txt --oneline",
	} {
		compareWithGit(t, gitDir, lineDir, c)
	}
}

func TestE2E_RenameDetectionMatchesGit(t *testing.T) {
	gitDir, lineDir := parityReposWith(t, func(dir string) {
		sh := func(script string) {
			cmd := exec.Command("bash", "-c", "set -e\n"+script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
				"GIT_AUTHOR_NAME=Ann Author", "GIT_AUTHOR_EMAIL=ann@example.com", "GIT_COMMITTER_NAME=Ann Author", "GIT_COMMITTER_EMAIL=ann@example.com",
				"GIT_AUTHOR_DATE=2026-03-01T10:00:00+0000", "GIT_COMMITTER_DATE=2026-03-01T10:00:00+0000")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		}
		// exact move across directories; edited move; two moves in one
		// commit; a delete that stays a delete; a rename with an exec bit
		sh(`seq 1 40 | sed 's/^/line /' > big.txt
mkdir -p pkg/deep
seq 1 30 | sed 's/^/deep /' > pkg/deep/d.txt
seq 1 20 | sed 's/^/other /' > other.txt
git add -A; git commit -q -m "add big files"
git mv big.txt big2.txt
sed -i 's/^line 20$/line twenty/; s/^line 21$/line twenty-one/' big2.txt
git mv pkg/deep/d.txt pkg/shallow/d.txt 2>/dev/null || { mkdir -p pkg/shallow; git mv pkg/deep/d.txt pkg/shallow/d.txt; }
git mv other.txt moved/other.txt 2>/dev/null || { mkdir -p moved; git mv other.txt moved/other.txt; }
git rm -q src/lib/lib.go
git add -A; git commit -q -m "renames: edited, moved dir, moved file, delete"
git mv moved/other.txt moved/other.sh; chmod +x moved/other.sh
seq 1 5 | sed 's/^/tiny /' > tiny.txt; git add -A; git commit -q -m "rename plus exec bit"
git mv tiny.txt tiny2.txt; sed -i 's/tiny 1/tiny one/; s/tiny 2/tiny two/; s/tiny 3/tiny three/' tiny2.txt
git add -A; git commit -q -m "below the threshold"`)
	})
	for _, c := range []string{
		"show --no-decorate --name-status HEAD~3",
		"show --no-decorate --name-status HEAD~2",
		"show --no-decorate --stat HEAD~2",
		"show --no-decorate --numstat HEAD~2",
		"show --no-decorate --shortstat HEAD~2",
		"show --no-decorate --summary HEAD~2",
		"show --no-decorate --stat --summary HEAD~2",
		"show --no-decorate -p HEAD~2",
		"show --no-decorate -p HEAD~1",
		"show --no-decorate -p HEAD",
		"show --no-decorate --name-status HEAD",
		"show --no-decorate --name-status -M10% HEAD",
		"show --no-decorate --name-status -M HEAD~1",
		"show --no-decorate --name-only HEAD~2",
		"show --no-decorate --name-status --no-renames HEAD~2",
		"show --no-decorate --name-status -M90% HEAD~2",
		"show --no-decorate --name-status -M9 HEAD~2",
		"show --no-decorate --name-status --find-renames=95% HEAD~2",
		"diff --name-status HEAD~3 HEAD",
		"diff --stat HEAD~3 HEAD",
		"diff -R --name-status HEAD~3 HEAD",
		"diff -R -p HEAD~2 HEAD~1",
		"diff --name-status HEAD~3 HEAD -- pkg",
		"diff --name-status HEAD~3 HEAD -- pkg/shallow",
		"log --oneline --name-status -4",
		"log --oneline --stat -4",
		"log --oneline --summary -4",
		"log --oneline --name-status --no-renames -4",
		"log --oneline -p -3 --full-index",
		"log -m --oneline --name-status",
		"show --no-decorate -m --stat HEAD~5",
		"show --no-decorate --stat HEAD~5",
		"show --no-decorate --name-status HEAD~5",
		"diff --dirstat HEAD~3 HEAD",
	} {
		compareWithGit(t, gitDir, lineDir, c)
	}
}

func TestE2E_StatusRenamesMatchGit(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	// the same moves on both sides (git: staged; cairn: on disk)
	mustGit(t, gitDir, "mv", "src/a.txt", "src/z.txt")
	mustGit(t, gitDir, "mv", "docs/blob.bin", "docs/blob2.bin")
	if err := os.WriteFile(filepath.Join(gitDir, "src", "z.txt"), []byte("one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\nelevenX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "fresh.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, gitDir, "add", "-A")
	for _, f := range [][2]string{{"src/a.txt", "src/z.txt"}, {"docs/blob.bin", "docs/blob2.bin"}} {
		if err := os.Rename(filepath.Join(lineDir, f[0]), filepath.Join(lineDir, f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lineDir, "src", "z.txt"), []byte("one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\nelevenX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lineDir, "fresh.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{
		"status --short",
		"status --porcelain",
		"status --porcelain -z",
		"status --porcelain=v2",
		"status --short --no-renames",
		"status --porcelain=v2 --no-renames",
		"diff --name-status HEAD",
		"diff --stat HEAD",
		"diff -p --full-index HEAD",
		"diff --summary --stat HEAD",
	} {
		args := strings.Fields(c)
		want, wantCode := gitRun(t, gitDir, args...)
		got, gotCode := cairnRun(t, lineDir, args...)
		if got != want || gotCode != wantCode {
			t.Errorf("%s (exit %d, git %d):\n--- cairn\n%s--- git\n%s", c, gotCode, wantCode, got, want)
		}
	}
}
