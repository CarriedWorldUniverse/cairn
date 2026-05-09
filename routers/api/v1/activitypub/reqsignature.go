// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package activitypub

import (
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	app_context "github.com/CarriedWorldUniverse/cairn/services/context"
	"github.com/CarriedWorldUniverse/cairn/services/federation"

	"github.com/42wim/httpsig"
)

func verifyHTTPSignature(ctx app_context.APIContext) (authenticated bool, err error) {
	if !setting.Federation.SignatureEnforced {
		return true, nil
	}

	r := ctx.Req

	// 1. Figure out what key we need to verify
	v, err := httpsig.NewVerifier(r)
	if err != nil {
		log.Debug("For %q verification failed: %v", r.URL.Path, err)
		return false, err
	}

	log.Debug("Verify %q, signed by KeyId: %v", r.URL.Path, v.KeyId())
	signatureAlgorithm := httpsig.Algorithm(setting.Federation.SignatureAlgorithms[0])
	pubKey, err := federation.FindOrCreateActorKey(ctx, v.KeyId())
	if err != nil {
		return false, err
	}

	err = v.Verify(pubKey, signatureAlgorithm)
	if err != nil {
		log.Debug("For %q verification failed: %v", r.URL.Path, err)
		return false, err
	}
	return true, nil
}

// ReqHTTPSignature function
func ReqHTTPSignature() func(ctx *app_context.APIContext) {
	return func(ctx *app_context.APIContext) {
		if authenticated, err := verifyHTTPSignature(*ctx); err != nil {
			log.Warn("verifyHttpSignature failed: %v", err)
			ctx.Error(http.StatusBadRequest, "reqSignature", "request signature verification failed")
		} else if !authenticated {
			ctx.Error(http.StatusForbidden, "reqSignature", "request signature verification failed")
		}
	}
}
