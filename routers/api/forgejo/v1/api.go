// Copyright 2023 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	"github.com/CarriedWorldUniverse/cairn/modules/web"
	"github.com/CarriedWorldUniverse/cairn/routers/api/shared"
)

func Routes() *web.Route {
	m := web.NewRoute()

	m.Use(shared.Middlewares()...)

	forgejo := NewForgejo()
	m.Get("", Root)
	m.Get("/version", forgejo.GetVersion)
	return m
}
