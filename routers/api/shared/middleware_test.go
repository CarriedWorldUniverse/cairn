// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package shared

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"forgejo.org/modules/json"
	"forgejo.org/modules/web"
	"forgejo.org/routers/common"
	"forgejo.org/services/authz"
	"forgejo.org/services/context"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReducer(t *testing.T) {
	makeRecorder := func() *httptest.ResponseRecorder {
		buff := bytes.NewBufferString("")
		recorder := httptest.NewRecorder()
		recorder.Body = buff
		return recorder
	}

	r := web.NewRoute()
	r.Use(common.ProtocolMiddlewares()...)
	r.Use(Middlewares()...)

	type ReducerInfo struct {
		IsSigned    bool
		IsNil       bool
		IsAllAccess bool
	}

	r.Get("/api/test", func(ctx *context.APIContext) {
		retval := ReducerInfo{
			IsSigned: ctx.IsSigned,
			IsNil:    ctx.Reducer == nil,
		}

		_, isAllAccess := ctx.Reducer.(*authz.AllAccessAuthorizationReducer)
		retval.IsAllAccess = isAllAccess

		ctx.JSON(http.StatusOK, retval)
	})

	// shared middleware ensures that `APIContext.Reducer` is not nil, and so that's the only test required in this
	// package's scope -- an anonymous request and `Reducer` is not nil:
	t.Run("anonymous", func(t *testing.T) {
		recorder := makeRecorder()
		req, err := http.NewRequest("GET", "http://localhost:8000/api/test", nil)
		require.NoError(t, err)
		r.ServeHTTP(recorder, req)
		assert.Equal(t, http.StatusOK, recorder.Code)

		var reducerInfo ReducerInfo
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &reducerInfo))

		assert.False(t, reducerInfo.IsSigned)
		assert.False(t, reducerInfo.IsNil)
		assert.True(t, reducerInfo.IsAllAccess)
	})
}
