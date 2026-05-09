// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/modules/web"
	"github.com/CarriedWorldUniverse/cairn/routers/api/actions/ping"
	"github.com/CarriedWorldUniverse/cairn/routers/api/actions/runner"
)

func Routes(prefix string) *web.Route {
	m := web.NewRoute()

	path, handler := ping.NewPingServiceHandler()
	m.Post(path+"*", http.StripPrefix(prefix, handler).ServeHTTP)

	path, handler = runner.NewRunnerServiceHandler()
	m.Post(path+"*", http.StripPrefix(prefix, handler).ServeHTTP)

	m.Mount("/.well-known", OIDCRoutes(prefix))
	m.Get(idTokenRouteBase, IDTokenContexter(), generateIDToken)

	return m
}
