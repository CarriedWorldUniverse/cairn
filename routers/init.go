// Copyright 2016 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package routers

import (
	"context"
	"errors"
	"reflect"
	"runtime"

	"github.com/CarriedWorldUniverse/cairn/models"
	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	"github.com/CarriedWorldUniverse/cairn/modules/cache"
	"github.com/CarriedWorldUniverse/cairn/modules/eventsource"
	"github.com/CarriedWorldUniverse/cairn/modules/git"
	"github.com/CarriedWorldUniverse/cairn/modules/highlight"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/markup"
	"github.com/CarriedWorldUniverse/cairn/modules/markup/external"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/ssh"
	"github.com/CarriedWorldUniverse/cairn/modules/storage"
	"github.com/CarriedWorldUniverse/cairn/modules/svg"
	"github.com/CarriedWorldUniverse/cairn/modules/system"
	"github.com/CarriedWorldUniverse/cairn/modules/templates"
	"github.com/CarriedWorldUniverse/cairn/modules/translation"
	"github.com/CarriedWorldUniverse/cairn/modules/web"
	actions_router "github.com/CarriedWorldUniverse/cairn/routers/api/actions"
	cairnv1 "github.com/CarriedWorldUniverse/cairn/routers/api/cairn/v1"
	forgejo "github.com/CarriedWorldUniverse/cairn/routers/api/forgejo/v1"
	packages_router "github.com/CarriedWorldUniverse/cairn/routers/api/packages"
	api_shared "github.com/CarriedWorldUniverse/cairn/routers/api/shared"
	apiv1 "github.com/CarriedWorldUniverse/cairn/routers/api/v1"
	"github.com/CarriedWorldUniverse/cairn/routers/common"
	"github.com/CarriedWorldUniverse/cairn/routers/private"
	web_routers "github.com/CarriedWorldUniverse/cairn/routers/web"
	actions_service "github.com/CarriedWorldUniverse/cairn/services/actions"
	auth_method "github.com/CarriedWorldUniverse/cairn/services/auth/method"
	"github.com/CarriedWorldUniverse/cairn/services/auth/source/oauth2"
	"github.com/CarriedWorldUniverse/cairn/services/automerge"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/services/cron"
	federation_service "github.com/CarriedWorldUniverse/cairn/services/federation"
	feed_service "github.com/CarriedWorldUniverse/cairn/services/feed"
	indexer_service "github.com/CarriedWorldUniverse/cairn/services/indexer"
	"github.com/CarriedWorldUniverse/cairn/services/mailer"
	mailer_incoming "github.com/CarriedWorldUniverse/cairn/services/mailer/incoming"
	markup_service "github.com/CarriedWorldUniverse/cairn/services/markup"
	migrations_service "github.com/CarriedWorldUniverse/cairn/services/migrations"
	mirror_service "github.com/CarriedWorldUniverse/cairn/services/mirror"
	pull_service "github.com/CarriedWorldUniverse/cairn/services/pull"
	release_service "github.com/CarriedWorldUniverse/cairn/services/release"
	repo_service "github.com/CarriedWorldUniverse/cairn/services/repository"
	"github.com/CarriedWorldUniverse/cairn/services/repository/archiver"
	"github.com/CarriedWorldUniverse/cairn/services/stats"
	"github.com/CarriedWorldUniverse/cairn/services/task"
	"github.com/CarriedWorldUniverse/cairn/services/uinotification"
	"github.com/CarriedWorldUniverse/cairn/services/webhook"
)

func mustInit(fn func() error) {
	err := fn()
	if err != nil {
		ptr := reflect.ValueOf(fn).Pointer()
		fi := runtime.FuncForPC(ptr)
		log.Fatal("%s failed: %v", fi.Name(), err)
	}
}

func mustInitCtx(ctx context.Context, fn func(ctx context.Context) error) {
	err := fn(ctx)
	if err != nil {
		ptr := reflect.ValueOf(fn).Pointer()
		fi := runtime.FuncForPC(ptr)
		log.Fatal("%s(ctx) failed: %v", fi.Name(), err)
	}
}

func syncAppConfForGit(ctx context.Context) error {
	runtimeState := new(system.RuntimeState)
	if err := system.AppState.Get(ctx, runtimeState); err != nil {
		return err
	}

	updated := false
	if runtimeState.LastAppPath != setting.AppPath {
		log.Info("AppPath changed from '%s' to '%s'", runtimeState.LastAppPath, setting.AppPath)
		runtimeState.LastAppPath = setting.AppPath
		updated = true
	}
	if runtimeState.LastCustomConf != setting.CustomConf {
		log.Info("CustomConf changed from '%s' to '%s'", runtimeState.LastCustomConf, setting.CustomConf)
		runtimeState.LastCustomConf = setting.CustomConf
		updated = true
	}

	if updated {
		return system.AppState.Set(ctx, runtimeState)
	}
	return nil
}

func InitWebInstallPage(ctx context.Context) {
	translation.InitLocales(ctx)
	setting.LoadSettingsForInstall()
	mustInit(svg.Init)
}

// InitWebInstalled is for global installed configuration.
func InitWebInstalled(ctx context.Context) {
	mustInitCtx(ctx, git.InitFull)
	log.Info("Git version: %s (home: %s)", git.VersionInfo(), git.HomeDir())

	// Setup i18n
	translation.InitLocales(ctx)

	setting.LoadSettings()
	mustInit(storage.Init)

	mailer.NewContext(ctx)
	mustInit(cache.Init)
	mustInit(feed_service.Init)
	mustInit(federation_service.Init)
	mustInit(uinotification.Init)
	mustInitCtx(ctx, archiver.Init)

	highlight.NewContext()
	external.RegisterRenderers()
	markup.Init(markup_service.ProcessorHelper())

	if setting.EnableSQLite3 {
		log.Info("SQLite3 support is enabled")
	} else if setting.Database.Type.IsSQLite3() {
		log.Fatal("SQLite3 support is disabled, but it is used for database setting. Please get or build a Forgejo release with SQLite3 support.")
	}

	mustInitCtx(ctx, common.InitDBEngine)
	log.Info("ORM engine initialization successful!")
	mustInit(system.Init)
	mustInitCtx(ctx, oauth2.Init)

	mustInit(release_service.Init)

	mustInitCtx(ctx, models.Init)
	mustInitCtx(ctx, auth_model.Init)
	mustInitCtx(ctx, repo_service.Init)

	if setting.Cairn.Enabled {
		mustInitCtx(ctx, initCairn)
	}

	// Booting long running goroutines.
	mustInit(indexer_service.Init)

	mirror_service.InitSyncMirrors()
	mustInit(webhook.Init)
	mustInit(pull_service.Init)
	mustInit(automerge.Init)
	mustInit(task.Init)
	mustInit(migrations_service.Init)
	eventsource.GetManager().Init()
	mustInitCtx(ctx, mailer_incoming.Init)

	mustInitCtx(ctx, syncAppConfForGit)

	mustInitCtx(ctx, ssh.Init)

	auth_method.Init()
	mustInit(svg.Init)

	actions_service.Init()
	mustInit(stats.Init)

	mustInit(actions_router.InitOIDC)

	// Finally start up the cron
	cron.NewContext(ctx)
}

// NormalRoutes represents non install routes
func NormalRoutes() *web.Route {
	_ = templates.HTMLRenderer()
	r := web.NewRoute()
	r.Use(common.ProtocolMiddlewares()...)

	r.Mount("/", web_routers.Routes())
	r.Mount("/api/v1", apiv1.Routes())
	r.Mount("/api/forgejo/v1", forgejo.Routes())
	r.Mount("/api/internal", private.Routes())

	if setting.Cairn.Enabled {
		r.Mount("/api/cairn/v1", cairnRoutes())
	}

	r.Post("/-/fetch-redirect", common.FetchRedirectDelegate)

	if setting.Packages.Enabled {
		// This implements package support for most package managers
		r.Mount("/api/packages", packages_router.CommonRoutes())
		// This implements the OCI API (Note this is not preceded by /api but is instead /v2)
		r.Mount("/v2", packages_router.ContainerRoutes())
	}

	if setting.Actions.Enabled {
		prefix := "/api/actions"
		r.Mount(prefix, actions_router.Routes(prefix))

		// TODO: Pipeline api used for runner internal communication with gitea server. but only artifact is used for now.
		// In Github, it uses ACTIONS_RUNTIME_URL=https://pipelines.actions.githubusercontent.com/fLgcSHkPGySXeIFrg8W8OBSfeg3b5Fls1A1CwX566g8PayEGlg/
		// TODO: this prefix should be generated with a token string with runner ?
		prefix = "/api/actions_pipeline"
		r.Mount(prefix, actions_router.ArtifactsRoutes(prefix))
		prefix = actions_router.ArtifactV4RouteBase
		r.Mount(prefix, actions_router.ArtifactsV4Routes(prefix))
	}

	return r
}

// initCairn loads the instance HMAC key and constructs the Cairn
// AgentService. Runs after models.Init so the xorm engine is live.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
func initCairn(ctx context.Context) error {
	// db.GetEngine(ctx) wraps the master engine into a per-context
	// session (*xorm.Session), which GetMasterEngine cannot unwrap.
	// Pull the master Engine from db.DefaultContext (set by
	// SetDefaultEngine during InitDBEngine) instead.
	engined, ok := db.DefaultContext.(db.Engined)
	if !ok {
		return errors.New("cairn: db.DefaultContext is not db.Engined")
	}
	masterEng, err := db.GetMasterEngine(engined.Engine())
	if err != nil {
		return err
	}
	return cairnv1.Init(
		ctx,
		setting.Cairn.HMACKeyPath,
		cairnidentity.NewXormAgentStore(masterEng),
		cairnidentity.NewXormBlocklistStore(masterEng),
		cairnv1.NewForgejoUserResolver(),
	)
}

// cairnRoutes builds the /api/cairn/v1 sub-router. Mirrors the shape
// of apiv1.Routes() — applies shared API middleware (which populates
// the APIContext + Doer) and then mounts Cairn's handlers via the
// RouteGroup adapter.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
func cairnRoutes() *web.Route {
	m := web.NewRoute()
	m.Use(api_shared.Middlewares()...)
	cairnv1.MountRoutes(cairnv1.NewForgejoRouteGroup(m))
	return m
}
