// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package method

import (
	"net/http"

	"forgejo.org/services/auth"
)

// Ensure the struct implements the interface.
var (
	_ auth.Method = &Group{}
)

// Group implements the Auth interface with serval Auth.
type Group struct {
	methods []auth.Method
}

// NewGroup creates a new auth group
func NewGroup(methods ...auth.Method) *Group {
	return &Group{
		methods: methods,
	}
}

// Add adds a new method to group
func (b *Group) Add(method auth.Method) {
	b.methods = append(b.methods, method)
}

func (b *Group) Verify(req *http.Request, w http.ResponseWriter, sess auth.SessionStore) (auth.AuthenticationResult, error) {
	// Try to sign in with each of the enabled plugins
	var retErr error
	for _, m := range b.methods {
		authResult, err := m.Verify(req, w, sess)
		if err != nil {
			if retErr == nil {
				retErr = err
			}
			// Try other methods if this one failed.
			// Some methods may share the same protocol to detect if they are matched.
			// For example, OAuth2 and conan.Auth both read token from "Authorization: Bearer <token>" header,
			// If OAuth2 returns error, we should give conan.Auth a chance to try.
			continue
		}

		// If any method returns an authenticated result, we can stop trying. Return and ignore any error returned by
		// previous methods.
		if authResult.User() != nil {
			return authResult, nil
		}
	}

	if retErr != nil {
		// If no method returns a user, return the error returned by the first method.
		return nil, retErr
	}

	return &auth.UnauthenticatedResult{}, nil
}
