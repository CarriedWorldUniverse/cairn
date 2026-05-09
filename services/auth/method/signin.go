// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package method

import (
	"context"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/models/auth"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/optional"
	auth_service "github.com/CarriedWorldUniverse/cairn/services/auth"
	"github.com/CarriedWorldUniverse/cairn/services/auth/source/oauth2"
	"github.com/CarriedWorldUniverse/cairn/services/auth/source/smtp"

	_ "github.com/CarriedWorldUniverse/cairn/services/auth/source/db"   // register the sources (and below)
	_ "github.com/CarriedWorldUniverse/cairn/services/auth/source/ldap" // register the ldap source
	_ "github.com/CarriedWorldUniverse/cairn/services/auth/source/pam"  // register the pam source
)

// UserSignIn validates user name and password.
func UserSignIn(ctx context.Context, username, password string) (*user_model.User, *auth.Source, error) {
	var user *user_model.User
	isEmail := false
	if strings.Contains(username, "@") {
		isEmail = true
		emailAddress := user_model.EmailAddress{LowerEmail: strings.ToLower(strings.TrimSpace(username))}
		// check same email
		has, err := db.GetEngine(ctx).Get(&emailAddress)
		if err != nil {
			return nil, nil, err
		}
		if has {
			if !emailAddress.IsActivated {
				return nil, nil, user_model.ErrEmailAddressNotExist{
					Email: username,
				}
			}
			user = &user_model.User{ID: emailAddress.UID}
		}
	} else {
		trimmedUsername := strings.TrimSpace(username)
		if len(trimmedUsername) == 0 {
			return nil, nil, user_model.ErrUserNotExist{Name: username}
		}

		user = &user_model.User{LowerName: strings.ToLower(trimmedUsername)}
	}

	if user != nil {
		hasUser, err := user_model.GetUser(ctx, user)
		if err != nil {
			return nil, nil, err
		}

		if hasUser {
			source, err := auth.GetSourceByID(ctx, user.LoginSource)
			if err != nil {
				return nil, nil, err
			}

			if !source.IsActive {
				return nil, nil, oauth2.ErrAuthSourceNotActivated
			}

			authenticator, ok := source.Cfg.(auth_service.PasswordAuthenticator)
			if !ok {
				return nil, nil, smtp.ErrUnsupportedLoginType
			}

			user, err := authenticator.Authenticate(ctx, user, user.LoginName, password)
			if err != nil {
				return nil, nil, err
			}

			// WARN: DON'T check user.IsActive, that will be checked on reqSign so that
			// user could be hint to resend confirm email.
			if user.ProhibitLogin {
				return nil, nil, user_model.ErrUserProhibitLogin{UID: user.ID, Name: user.Name}
			}

			return user, source, nil
		}
	}

	sources, err := db.Find[auth.Source](ctx, auth.FindSourcesOptions{
		IsActive: optional.Some(true),
	})
	if err != nil {
		return nil, nil, err
	}

	for _, source := range sources {
		if !source.IsActive {
			// don't try to authenticate non-active sources
			continue
		}

		authenticator, ok := source.Cfg.(auth_service.PasswordAuthenticator)
		if !ok {
			continue
		}

		authUser, err := authenticator.Authenticate(ctx, nil, username, password)

		if err == nil {
			if !authUser.ProhibitLogin {
				return authUser, source, nil
			}
			err = user_model.ErrUserProhibitLogin{UID: authUser.ID, Name: authUser.Name}
		}

		if user_model.IsErrUserNotExist(err) {
			log.Debug("Failed to login '%s' via '%s': %v", username, source.Name, err)
		} else {
			log.Warn("Failed to login '%s' via '%s': %v", username, source.Name, err)
		}
	}

	if isEmail {
		return nil, nil, user_model.ErrEmailAddressNotExist{Email: username}
	}

	return nil, nil, user_model.ErrUserNotExist{Name: username}
}
