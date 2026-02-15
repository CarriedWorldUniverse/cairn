// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package authz

import (
	"context"

	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"

	"xorm.io/builder"
)

// Defines an API for reducing available permissions to specific resources.  Typically associated with a fine-grained
// access tokens and provides methods to reduce authorization that the access token provides down to specific resources.
type AuthorizationReducer interface {
	// Given a repository and an accessMode, ReduceRepoAccess will return a new, possibly reduced, AccessMode that
	// reflects the actual access that is currently permitted.  For example, when using a fine-grained access token that
	// only grants write access to one target repository, `ReduceRepoAccess(target, AccessModeWrite)` would return
	// `AccessModeWrite`, and `ReduceRepoAccess(other-repo, AccessModeWrite)` would return a lesser access mode,
	// restricting access to other repositories.
	ReduceRepoAccess(ctx context.Context, repo *repo_model.Repository, accessMode perm.AccessMode) (perm.AccessMode, error)

	// If querying the repository table, apply this condition to return only repositories that the restriction will
	// allow the target access mode (or higher).  For example, when using a fine-grained access token that only grants
	// write access to one target repository, `RepoFilter(AccessModeWrite)` would return a filter that only returns that
	// single repository, while `RepoFilter(AccessModeRead)` would return a filter that includes all public repositories
	// and the target repository.
	RepoFilter(accessMode perm.AccessMode) builder.Cond

	// Controls whether the presence of an authorization reducer will prevent administrators from overriding permission
	// checks. Typically site administrators and repo administrators are exempted from permission checks, but if an
	// authorization reducer is present then it may be intended for its restrictions to apply even to administrators.
	//
	// `true` allows the typical case where administrators *can* override permissions. `false` disables administrator
	// overrides of permission checks.
	AllowAdminOverride() bool
}
