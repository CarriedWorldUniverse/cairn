// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth_test

import (
	"strings"
	"testing"

	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	"github.com/CarriedWorldUniverse/cairn/modules/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/xorm/schemas"
)

type TestSource struct {
	Provider                      string
	ClientID                      string
	ClientSecret                  string
	OpenIDConnectAutoDiscoveryURL string
	IconURL                       string
}

// FromDB fills up a LDAPConfig from serialized format.
func (source *TestSource) FromDB(bs []byte) error {
	return json.Unmarshal(bs, &source)
}

// ToDB exports a LDAPConfig to a serialized format.
func (source *TestSource) ToDB() ([]byte, error) {
	return json.Marshal(source)
}

func TestDumpAuthSource(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	authSourceSchema, err := db.TableInfo(new(auth_model.Source))
	require.NoError(t, err)

	auth_model.RegisterTypeConfig(auth_model.OAuth2, new(TestSource))

	auth_model.CreateSource(db.DefaultContext, &auth_model.Source{
		Type:     auth_model.OAuth2,
		Name:     "TestSource",
		IsActive: false,
		Cfg: &TestSource{
			Provider: "ConvertibleSourceName",
			ClientID: "42",
		},
	})

	sb := new(strings.Builder)

	db.DumpTables([]*schemas.Table{authSourceSchema}, sb)

	assert.Contains(t, sb.String(), `"Provider":"ConvertibleSourceName"`)
}
