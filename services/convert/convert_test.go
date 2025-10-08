// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	api "forgejo.org/modules/structs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToVerification(t *testing.T) {
	defer unittest.OverrideFixtures("models/fixtures/TestParseCommitWithSSHSignature")()
	require.NoError(t, unittest.PrepareTestDatabase())

	// Change the user's primary email address to ensure this value isn't ambiguous with any other return value from
	// signature verification.
	userModel := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	userModel.Email = "secret-email@example.com"
	db.GetEngine(t.Context()).ID(userModel.ID).Cols("email").Update(userModel)

	t.Run("SSH Key Signature", func(t *testing.T) {
		commit := &git.Commit{
			Committer: &git.Signature{
				Email: "user2@example.com",
			},
			Signature: &git.ObjectSignature{
				Payload: `tree 853694aae8816094a0d875fee7ea26278dbf5d0f
parent c2780d5c313da2a947eae22efd7dacf4213f4e7f
author user2 <user2@example.com> 1699707877 +0100
committer user2 <user2@example.com> 1699707877 +0100

Add content
`,
				Signature: `-----BEGIN SSH SIGNATURE-----
U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgoGSe9Zy7Ez9bSJcaTNjh/Y7p95
f5DujjqkpzFRtw6CEAAAADZ2l0AAAAAAAAAAZzaGE1MTIAAABTAAAAC3NzaC1lZDI1NTE5
AAAAQBe2Fwk/FKY3SBCnG6jSYcO6ucyahp2SpQ/0P+otslzIHpWNW8cQ0fGLdhhaFynJXQ
fs9cMpZVM9BfIKNUSO8QY=
-----END SSH SIGNATURE-----
`,
			},
		}
		commitVerification := ToVerification(t.Context(), commit)
		require.NotNil(t, commitVerification)
		assert.Equal(t, &api.PayloadCommitVerification{
			Verified:  true,
			Reason:    "user2 / SHA256:TKfwbZMR7e9OnlV2l1prfah1TXH8CmqR0PvFEXVCXA4",
			Signature: "-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgoGSe9Zy7Ez9bSJcaTNjh/Y7p95\nf5DujjqkpzFRtw6CEAAAADZ2l0AAAAAAAAAAZzaGE1MTIAAABTAAAAC3NzaC1lZDI1NTE5\nAAAAQBe2Fwk/FKY3SBCnG6jSYcO6ucyahp2SpQ/0P+otslzIHpWNW8cQ0fGLdhhaFynJXQ\nfs9cMpZVM9BfIKNUSO8QY=\n-----END SSH SIGNATURE-----\n",
			Signer: &api.PayloadUser{
				Name:  "user2",
				Email: "user2@example.com", // expected email will match the commit's committer's email, regardless of `KeepEmailPrivate`.
			},
			Payload: "tree 853694aae8816094a0d875fee7ea26278dbf5d0f\nparent c2780d5c313da2a947eae22efd7dacf4213f4e7f\nauthor user2 <user2@example.com> 1699707877 +0100\ncommitter user2 <user2@example.com> 1699707877 +0100\n\nAdd content\n",
		}, commitVerification)
	})

	t.Run("GPG Signature", func(t *testing.T) {
		commit := &git.Commit{
			ID: git.MustIDFromString("e20aa0bcd2878f65a93de68a3eed9045d6efdd74"),
			Committer: &git.Signature{
				Email: "user2@example.com",
			},
			Signature: &git.ObjectSignature{
				Payload: `tree e20aa0bcd2878f65a93de68a3eed9045d6efdd74
parent 5cd9b9847563eb730d63d23c1f1b84868e52ae7d
author user2 <user2+committer@example.com> 1759956520 -0600
committer user2 <user2+committer@example.com> 1759956520 -0600

Add content
`,
				Signature: `-----BEGIN PGP SIGNATURE-----

iQEzBAABCgAdFiEEdlqhn25IEoMmvK5vmDaXTfEZWRMFAmjmzigACgkQmDaXTfEZ
WROC4ggAs8mD8csA6FV5e2v/4HcxuaZKCN+D8Gvku2JUigODQCA+NOX0FF2jDnCh
tXylBPB4HJw1spKkDLtOpnCUSOniBdl9NcZjnBt6sP/OSnEfLznXFra+9fCHzsu0
9uhDn3Wn1iHWXQ2ZglUwVS0ja6pNgEip8wNZBysv8+XbO1CEEW0m7zQA6tunzIwp
yiPZDUJrKtpKAK0+v19EccT2VjYAa+Vo+p3/E0piaTYNbsTqtFRy63tdjDkf+mo+
l/PaPhrMqdnbxv3/sd/63VCNdvPH3f0+OuydcC7mXyysmvap99EC+QKnpsrm7RAP
uf51WIBywxztet6vi+jYJK1jFoY4iA==
=Lnrt
-----END PGP SIGNATURE-----`,
			},
		}
		commitVerification := ToVerification(t.Context(), commit)
		require.NotNil(t, commitVerification)
		assert.Equal(t, &api.PayloadCommitVerification{
			Verified:  true,
			Reason:    "user2 / 9836974DF1195913",
			Signature: "-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCgAdFiEEdlqhn25IEoMmvK5vmDaXTfEZWRMFAmjmzigACgkQmDaXTfEZ\nWROC4ggAs8mD8csA6FV5e2v/4HcxuaZKCN+D8Gvku2JUigODQCA+NOX0FF2jDnCh\ntXylBPB4HJw1spKkDLtOpnCUSOniBdl9NcZjnBt6sP/OSnEfLznXFra+9fCHzsu0\n9uhDn3Wn1iHWXQ2ZglUwVS0ja6pNgEip8wNZBysv8+XbO1CEEW0m7zQA6tunzIwp\nyiPZDUJrKtpKAK0+v19EccT2VjYAa+Vo+p3/E0piaTYNbsTqtFRy63tdjDkf+mo+\nl/PaPhrMqdnbxv3/sd/63VCNdvPH3f0+OuydcC7mXyysmvap99EC+QKnpsrm7RAP\nuf51WIBywxztet6vi+jYJK1jFoY4iA==\n=Lnrt\n-----END PGP SIGNATURE-----",
			Signer: &api.PayloadUser{
				Name:  "user2",
				Email: "user2+signingkey@example.com", // expected email will match the signing key's email
			},
			Payload: "tree e20aa0bcd2878f65a93de68a3eed9045d6efdd74\nparent 5cd9b9847563eb730d63d23c1f1b84868e52ae7d\nauthor user2 <user2+committer@example.com> 1759956520 -0600\ncommitter user2 <user2+committer@example.com> 1759956520 -0600\n\nAdd content\n",
		}, commitVerification)
	})
}
