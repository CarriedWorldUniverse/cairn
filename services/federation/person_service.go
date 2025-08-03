// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package federation

import (
	"context"

	"forgejo.org/models/user"
	"forgejo.org/modules/log"

	ap "github.com/go-ap/activitypub"
)

func ProcessPersonInbox(ctx context.Context, user *user.User, activity *ap.Activity) (ServiceResult, error) {
	switch activity.Type {
	case ap.FollowType:
		return processPersonFollow(ctx, user, activity)
	case ap.UndoType:
		return processPersonInboxUndo(ctx, user, activity)
	}

	log.Error("Unsupported PersonInbox activity: %v", activity.Type)
	return ServiceResult{}, NewErrNotAcceptablef("unsupported activity: %v", activity.Type)
}
