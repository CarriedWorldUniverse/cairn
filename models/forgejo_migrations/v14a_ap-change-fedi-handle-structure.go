// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"context"
	"fmt"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/models/forgefed"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/validation"
	user_service "forgejo.org/services/user"

	"xorm.io/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "use structure @PreferredUsername@host.tld:port for actors",
		Upgrade:     changeActivityPubUsernameFormat,
	})
}

func changeActivityPubUsernameFormat(x *xorm.Engine) error {
	// Normally, the db.WithTx statement ensures that the database transaction (aka. all changes made
	// by this migration) will only be committed if the SQL operations inside of the iteration
	// (db.Iterate) don't return an error.
	//
	// This migration was originally authored with those cases in mind, but it was later agreed that
	// migrations concerning Forgejo's federation-related components should not return any errors at
	// this point in time, as federation is not considered to be stable at the moment. For more
	// information, check the relevant discussion here:
	// https://codeberg.org/forgejo-contrib/federation/issues/67
	//
	// Nevertheless, this structure involves some useful boilerplate that can be used for future
	// migrations at a later point and has been kept as-is.
	return db.WithTx(db.DefaultContext, func(ctx context.Context) error {
		// The transaction is committed only if modifying all federated users is possible.
		return db.Iterate(ctx, nil, func(ctx context.Context, federatedUser *user_model.FederatedUser) error {
			// localUser represents the "local" representation of an ActivityPub (federated) user
			localUser := &user_model.User{}
			has, err := db.GetEngine(ctx).ID(federatedUser.UserID).Get(localUser)
			if err != nil {
				log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred while getting local user (ID: %d), ignoring...: %e", federatedUser.UserID, err)
				return nil
			}

			if !has {
				log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: User missing for federated user: %v", federatedUser)
				err := user_model.DeleteFederatedUser(ctx, federatedUser.UserID)
				if err != nil {
					log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred while deleting federated user (%s), ignoring...: %e", federatedUser, err)
					return nil
				}
			}

			if validation.IsValidActivityPubUsername(localUser.Name) {
				log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: FederatedUser was already migrated: %v", federatedUser)
			} else {
				// Copied from models/forgefed/federationhost_repository.go (forgefed.GetFederationHost),
				// minus some validation code for FederationHost which we do not otherwise manipulate here.
				federationHost := new(forgefed.FederationHost)
				has, err := db.GetEngine(ctx).ID(federatedUser.FederationHostID).Get(federationHost)
				if err != nil {
					log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred while looking up federation host info (for %v), ignoring...: %e", federatedUser, err)
					return nil
				} else if !has {
					log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Federation host for federated user missing, deleting: %v", federatedUser)
					err := user_model.DeleteFederatedUser(ctx, federatedUser.UserID)
					if err != nil {
						log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred while deleting federated user (%v), ignoring...: %e", federatedUser, err)
						return nil
					}

					err = user_service.DeleteUser(ctx, localUser, true)
					if err != nil {
						log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred while deleting user (%s), ignoring...: %v", localUser.LogString(), err)
					}

					return nil
				}

				// Take part of the username before the first dash, reconstruct the rest
				// of it using whatever we have in FederationHost. Before this migration,
				// usernames of ActivityPub accounts have an expected format of
				// "username-subdomain-domain-tld-port". We don't know how many subdomains
				// there are, but that doesn't matter. We can always get the username unless
				// if the username of an ActivityPub account was manually changed by an admin,
				// in which case they should either delete the account or change it back.
				s := strings.Split(localUser.Name, "-")
				if len(s) == 0 {
					log.Warn(
						"Migration[v14a_ap-change-fedi-handle-structure]: Username %s belonging to federatedUser %v does not contain any dashes, can't construct new username",
						localUser.Name,
						federatedUser,
					)
					return nil
				}

				// Were a running Forgejo instance to create a new federated account, would the port
				// have been marked as "supplemented" (thus leading to its omission)?
				var newUsername string
				if (federationHost.HostPort == 443 && federationHost.HostSchema == "https") || (federationHost.HostPort == 80 && federationHost.HostSchema == "http") {
					newUsername = fmt.Sprintf("@%s@%s", s[0], federationHost.HostFqdn)
				} else {
					newUsername = fmt.Sprintf("@%s@%s:%d", s[0], federationHost.HostFqdn, federationHost.HostPort)
				}

				// Implicitly assumes that there won't be a lower name unique constraint violation.
				// Potentially a bit paranoid, but why not?
				userThatShouldntExist := &user_model.User{}
				lowernameTaken, err := db.GetEngine(ctx).Where("lower_name = ?", strings.ToLower(newUsername)).Table("user").Get(userThatShouldntExist)
				if err != nil {
					log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred, skipping migration of %s: %e", localUser.LogString(), err)
					return nil
				}

				if lowernameTaken {
					log.Warn(
						"Migration[v14a_ap-change-fedi-handle-structure]: New username %s for %s already taken by %s, deleting the former...",
						newUsername,
						localUser.LogString(),
						userThatShouldntExist.LogString(),
					)
					err := user_model.DeleteFederatedUser(ctx, localUser.ID)
					if err != nil {
						log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred while deleting federated user (%s), ignoring...: %e", localUser.LogString(), err)
					}
					return nil
				}

				// Safe to assume that the following operations should just work now.
				log.Info("Migration[v14a_ap-change-fedi-handle-structure]: Updating username of %s to %s", localUser.LogString(), newUsername)
				if _, err := db.GetEngine(ctx).ID(localUser.ID).Cols("lower_name", "name").Update(&user_model.User{
					LowerName: strings.ToLower(newUsername),
					Name:      newUsername,
				}); err != nil {
					log.Warn("Migration[v14a_ap-change-fedi-handle-structure]: Database error occurred when updating federated user's username (%s), ignoring...: %e", localUser.LogString(), err)
					return nil
				}
			}

			return nil
		})
	})
}
