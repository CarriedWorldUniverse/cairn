//go:build windows

package main

import "os"

// ownedByCurrentUser: file ownership is not checked on Windows (ACL-based);
// repo config is honoured, as the other defences (reserved .cairn names,
// strict repo discovery) do not depend on it.
func ownedByCurrentUser(os.FileInfo) bool { return true }
