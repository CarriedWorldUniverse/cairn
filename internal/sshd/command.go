package sshd

import (
	"fmt"
	"strings"
)

// ParseGitCommand parses an SSH exec command of the form
//
//	git-upload-pack '/org/slug.git'
//	git-receive-pack '/org/slug'
//
// into (verb, org, slug). Only the two pack verbs are allowed; anything else
// (shell access, scp, etc.) is rejected.
func ParseGitCommand(cmd string) (verb, org, slug string, err error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", "", "", fmt.Errorf("sshd: empty command")
	}
	sp := strings.IndexByte(cmd, ' ')
	if sp < 0 {
		return "", "", "", fmt.Errorf("sshd: command %q missing path", cmd)
	}
	verb = cmd[:sp]
	if verb != "git-upload-pack" && verb != "git-receive-pack" {
		return "", "", "", fmt.Errorf("sshd: unsupported command %q", verb)
	}
	path := strings.TrimSpace(cmd[sp+1:])
	path = strings.Trim(path, `'"`)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("sshd: path %q must be /org/slug", path)
	}
	return verb, parts[0], parts[1], nil
}
