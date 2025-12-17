// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"context"

	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"

	"xorm.io/builder"
	"xorm.io/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "Set ProhibitLogin and UserTypeActivityPubUser for remote users created from ActivityPub.",
		Upgrade:     setProhibitLoginActivityPubUser,
	})
}

func setProhibitLoginActivityPubUser(x *xorm.Engine) error {
	return db.WithTx(db.DefaultContext, func(ctx context.Context) error {
		return db.Iterate(ctx, builder.Eq{"type": 5}, func(ctx context.Context, user *user_model.User) error {
			log.Info("Checking if user %s is created from ActivityPub", user.LogString())

			// Users created from f3 also have the RemoteUser user type. All
			// FederatedUser should reference exactly one User.
			has, err := db.GetEngine(ctx).Table("federated_user").Get(&user_model.FederatedUser{UserID: user.ID})
			if err != nil {
				return err
			}

			if !has {
				return nil
			}

			log.Info("Updating user %s", user.LogString())
			_, err = db.GetEngine(ctx).Table("user").ID(user.ID).Cols("type", "prohibit_login", "passwd", "salt", "passwd_hash_algo").Update(&user_model.User{
				Type:           user_model.UserTypeActivityPubUser,
				ProhibitLogin:  true,
				Passwd:         "",
				Salt:           "",
				PasswdHashAlgo: "",
			})

			return err
		})
	})
}
