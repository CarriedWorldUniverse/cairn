// Copyright Earl Warren <contact@earl-warren.org>
// Copyright Loïc Dachary <loic@dachary.org>
// SPDX-License-Identifier: MIT

package tests

import (
	"testing"

	forgejo_log "github.com/CarriedWorldUniverse/cairn/modules/log"
	driver_options "github.com/CarriedWorldUniverse/cairn/services/f3/driver/options"
	"github.com/CarriedWorldUniverse/cairn/services/f3/util"

	"code.forgejo.org/f3/gof3/v3/options"
)

func newTestOptions(_ *testing.T) options.Interface {
	o := options.GetFactory(driver_options.Name)().(*driver_options.Options)
	l := forgejo_log.GetLogger(forgejo_log.DEFAULT)
	o.SetLogger(util.NewF3Logger(nil, l))
	return o
}
