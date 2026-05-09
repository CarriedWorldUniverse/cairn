package v1

import (
	"context"

	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// forgejoUserResolver implements cairnidentity.UserResolver against
// Forgejo's user model. Returns cairnidentity.ErrUserNotFound when
// the requested user does not exist.
type forgejoUserResolver struct{}

// NewForgejoUserResolver returns a UserResolver that looks up users
// in Forgejo's standard user table.
func NewForgejoUserResolver() cairnidentity.UserResolver {
	return &forgejoUserResolver{}
}

func (r *forgejoUserResolver) UserIDByUsername(ctx context.Context, name string) (int64, error) {
	u, err := user_model.GetUserByName(ctx, name)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return 0, cairnidentity.ErrUserNotFound
		}
		return 0, err
	}
	return u.ID, nil
}

func (r *forgejoUserResolver) UsernameByID(ctx context.Context, id int64) (string, error) {
	u, err := user_model.GetUserByID(ctx, id)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return "", cairnidentity.ErrUserNotFound
		}
		return "", err
	}
	return u.Name, nil
}
