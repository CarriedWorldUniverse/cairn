// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"github.com/CarriedWorldUniverse/cairn/modules/graceful"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/queue"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	notify_service "github.com/CarriedWorldUniverse/cairn/services/notify"
)

func Init() {
	if !setting.Actions.Enabled {
		return
	}

	jobEmitterQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "actions_ready_job", jobEmitterQueueHandler)
	if jobEmitterQueue == nil {
		log.Fatal("Unable to create actions_ready_job queue")
	}
	go graceful.GetManager().RunWithCancel(jobEmitterQueue)

	notify_service.RegisterNotifier(NewNotifier())
}
