// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"encoding/json"
	"errors"
	"net/http"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	"github.com/CarriedWorldUniverse/cairn/models/perm/access"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const (
	maxConfigBody  = 4096
	maxConsentBody = 1024
)

// SummarizerConfigRequest is the wire format for PUT
// /api/cairn/v1/orgs/{owner}/summarizer.
type SummarizerConfigRequest struct {
	Enabled       bool   `json:"enabled"`
	Provider      string `json:"provider"`
	EndpointURL   string `json:"endpoint_url"`
	ModelID       string `json:"model_id"`
	APIKey        string `json:"api_key"`
	LevelsEnabled int    `json:"levels_enabled"`
}

// SummarizerConfigResponse is the wire format for GET/PUT
// /api/cairn/v1/orgs/{owner}/summarizer. Credentials are never returned;
// only a boolean indicating whether a credential is stored.
type SummarizerConfigResponse struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	EndpointURL    string `json:"endpoint_url"`
	ModelID        string `json:"model_id"`
	CredentialsSet bool   `json:"credentials_set"`
	LevelsEnabled  int    `json:"levels_enabled"`
}

// RepoConsentRequest is the wire format for PUT
// /api/cairn/v1/repos/{owner}/{repo}/summarizer.
type RepoConsentRequest struct {
	Enabled   bool                  `json:"enabled"`
	DataScope cairnmodels.DataScope `json:"data_scope"`
}

// RepoConsentResponse is the wire format for GET/PUT repo consent.
type RepoConsentResponse struct {
	Enabled   bool                  `json:"enabled"`
	DataScope cairnmodels.DataScope `json:"data_scope"`
}

// SummaryResponse is the wire format for GET cached PR summary.
type SummaryResponse struct {
	SummaryMD   string `json:"summary_md"`
	ModelID     string `json:"model_id"`
	GeneratedAt int64  `json:"generated_at"`
}

// configToResponse renders a SummarizerConfig as the public response,
// stripping credentials. Pure helper — pulled out so the redaction
// guarantee can be unit-tested without spinning up an APIContext.
func configToResponse(cfg *cairnmodels.SummarizerConfig) SummarizerConfigResponse {
	if cfg == nil {
		return SummarizerConfigResponse{}
	}
	return SummarizerConfigResponse{
		Enabled:        cfg.Enabled,
		Provider:       cfg.Provider,
		EndpointURL:    cfg.EndpointURL,
		ModelID:        cfg.ModelID,
		CredentialsSet: len(cfg.CredentialsCipher) > 0,
		LevelsEnabled:  int(cfg.LevelsEnabled),
	}
}

// resolveOwnerForSummarizer loads the owner user named in :owner and
// enforces the org-config auth rule (caller must be that user or a
// site admin). Returns nil after writing an error response on failure.
func resolveOwnerForSummarizer(ctx *context.APIContext) *user_model.User {
	if ctx.Doer == nil {
		ctx.Error(http.StatusUnauthorized, "unauthenticated", nil)
		return nil
	}
	name := ctx.Params(":owner")
	owner, err := user_model.GetUserByName(ctx, name)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			ctx.Error(http.StatusNotFound, "owner not found", nil)
			return nil
		}
		ctx.Error(http.StatusInternalServerError, "GetUserByName", err)
		return nil
	}
	if !ctx.Doer.IsAdmin && ctx.Doer.ID != owner.ID {
		ctx.Error(http.StatusForbidden, "admin required", nil)
		return nil
	}
	return owner
}

// GetSummarizerConfig — GET /api/cairn/v1/orgs/{owner}/summarizer.
func GetSummarizerConfig(ctx *context.APIContext) {
	owner := resolveOwnerForSummarizer(ctx)
	if owner == nil {
		return
	}
	cfg := &cairnmodels.SummarizerConfig{}
	has, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Get(cfg)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load config", err)
		return
	}
	if !has {
		ctx.JSON(http.StatusOK, SummarizerConfigResponse{})
		return
	}
	ctx.JSON(http.StatusOK, configToResponse(cfg))
}

// PutSummarizerConfig — PUT /api/cairn/v1/orgs/{owner}/summarizer.
func PutSummarizerConfig(ctx *context.APIContext) {
	owner := resolveOwnerForSummarizer(ctx)
	if owner == nil {
		return
	}

	var req SummarizerConfigRequest
	ctx.Req.Body = http.MaxBytesReader(ctx.Resp, ctx.Req.Body, maxConfigBody)
	if err := json.NewDecoder(ctx.Req.Body).Decode(&req); err != nil {
		ctx.Error(http.StatusBadRequest, "decode body", err)
		return
	}

	existing := &cairnmodels.SummarizerConfig{}
	has, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Get(existing)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load config", err)
		return
	}

	cfg := &cairnmodels.SummarizerConfig{
		OwnerID:       owner.ID,
		Enabled:       req.Enabled,
		Provider:      req.Provider,
		EndpointURL:   req.EndpointURL,
		ModelID:       req.ModelID,
		LevelsEnabled: cairnmodels.LevelFlag(req.LevelsEnabled),
	}

	if req.APIKey != "" {
		hmacKey := summarizer.HMACKey()
		if hmacKey == nil {
			ctx.Error(http.StatusServiceUnavailable, "summarizer not initialized", nil)
			return
		}
		cipher, err := summarizer.EncryptCredential(hmacKey, []byte(req.APIKey))
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "encrypt", err)
			return
		}
		cfg.CredentialsCipher = cipher
	} else if has {
		cfg.CredentialsCipher = existing.CredentialsCipher
	}

	if has {
		// AllCols ensures zero-valued booleans (Enabled=false) actually
		// write through xorm's update path.
		if _, err := db.GetEngine(ctx).ID(owner.ID).AllCols().Update(cfg); err != nil {
			ctx.Error(http.StatusInternalServerError, "update", err)
			return
		}
	} else {
		if _, err := db.GetEngine(ctx).Insert(cfg); err != nil {
			ctx.Error(http.StatusInternalServerError, "insert", err)
			return
		}
	}

	ctx.JSON(http.StatusOK, configToResponse(cfg))
}

// resolveRepoForConsent loads the repo named in :owner/:repo, enforces
// repo-admin permission, and rejects public repos with 400. Returns
// nil after writing an error response on failure.
func resolveRepoForConsent(ctx *context.APIContext) *repo_model.Repository {
	if ctx.Doer == nil {
		ctx.Error(http.StatusUnauthorized, "unauthenticated", nil)
		return nil
	}
	owner, err := user_model.GetUserByName(ctx, ctx.Params(":owner"))
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			ctx.Error(http.StatusNotFound, "owner not found", nil)
			return nil
		}
		ctx.Error(http.StatusInternalServerError, "GetUserByName", err)
		return nil
	}
	repo, err := repo_model.GetRepositoryByName(ctx, owner.ID, ctx.Params(":repo"))
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			ctx.Error(http.StatusNotFound, "repo not found", nil)
			return nil
		}
		ctx.Error(http.StatusInternalServerError, "GetRepositoryByName", err)
		return nil
	}
	// Permission check FIRST — non-admins must not learn whether a
	// repo is private vs public vs nonexistent. Returning 400
	// "consent only applies to private repos" before checking the
	// caller's access leaked the IsPrivate bit to anyone who could
	// guess a repo name. Forgejo's standard non-disclosure pattern
	// is 404 for unauthorized callers regardless of repo visibility;
	// only admins reach the IsPrivate gate below.
	perm, err := access.GetUserRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetUserRepoPermission", err)
		return nil
	}
	if !perm.IsAdmin() && !ctx.Doer.IsAdmin {
		ctx.Error(http.StatusNotFound, "repo not found", nil)
		return nil
	}
	if !repo.IsPrivate {
		ctx.Error(http.StatusBadRequest, "consent only applies to private repos", nil)
		return nil
	}
	return repo
}

// GetRepoConsent — GET /api/cairn/v1/repos/{owner}/{repo}/summarizer.
func GetRepoConsent(ctx *context.APIContext) {
	repo := resolveRepoForConsent(ctx)
	if repo == nil {
		return
	}
	consent := &cairnmodels.SummarizerRepoConsent{}
	has, err := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Get(consent)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load consent", err)
		return
	}
	if !has {
		ctx.JSON(http.StatusOK, RepoConsentResponse{
			Enabled:   false,
			DataScope: cairnmodels.DataScopeMetadata,
		})
		return
	}
	ctx.JSON(http.StatusOK, RepoConsentResponse{
		Enabled:   consent.Enabled,
		DataScope: consent.DataScope,
	})
}

// PutRepoConsent — PUT /api/cairn/v1/repos/{owner}/{repo}/summarizer.
func PutRepoConsent(ctx *context.APIContext) {
	repo := resolveRepoForConsent(ctx)
	if repo == nil {
		return
	}

	var req RepoConsentRequest
	ctx.Req.Body = http.MaxBytesReader(ctx.Resp, ctx.Req.Body, maxConsentBody)
	if err := json.NewDecoder(ctx.Req.Body).Decode(&req); err != nil {
		ctx.Error(http.StatusBadRequest, "decode body", err)
		return
	}
	if req.Enabled && !req.DataScope.IsValid() {
		ctx.Error(http.StatusBadRequest, "invalid data_scope", nil)
		return
	}

	consent := &cairnmodels.SummarizerRepoConsent{
		RepoID:    repo.ID,
		Enabled:   req.Enabled,
		DataScope: req.DataScope,
	}
	existing := &cairnmodels.SummarizerRepoConsent{}
	has, err := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Get(existing)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load consent", err)
		return
	}
	if has {
		_, err = db.GetEngine(ctx).ID(repo.ID).AllCols().Update(consent)
	} else {
		_, err = db.GetEngine(ctx).Insert(consent)
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "save consent", err)
		return
	}
	ctx.JSON(http.StatusOK, RepoConsentResponse{
		Enabled:   consent.Enabled,
		DataScope: consent.DataScope,
	})
}

// resolveRepoForRead loads the repo named in :owner/:repo and enforces
// read permission. Used for the cached-summary GET endpoint.
//
// The explicit ctx.Doer == nil guard distinguishes "anonymous" from
// "wrong repo" for clients (otherwise both collapse to 404 via
// HasAccess on private repos). PostRegenerate has the same guard at
// its top level — keeping it here too means future callers can't
// bypass authentication by reusing this resolver.
func resolveRepoForRead(ctx *context.APIContext) *repo_model.Repository {
	if ctx.Doer == nil {
		ctx.Error(http.StatusUnauthorized, "authentication required", nil)
		return nil
	}
	owner, err := user_model.GetUserByName(ctx, ctx.Params(":owner"))
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			ctx.Error(http.StatusNotFound, "owner not found", nil)
			return nil
		}
		ctx.Error(http.StatusInternalServerError, "GetUserByName", err)
		return nil
	}
	repo, err := repo_model.GetRepositoryByName(ctx, owner.ID, ctx.Params(":repo"))
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			ctx.Error(http.StatusNotFound, "repo not found", nil)
			return nil
		}
		ctx.Error(http.StatusInternalServerError, "GetRepositoryByName", err)
		return nil
	}
	perm, err := access.GetUserRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetUserRepoPermission", err)
		return nil
	}
	if !perm.HasAccess() {
		ctx.Error(http.StatusNotFound, "repo not found", nil)
		return nil
	}
	return repo
}

// GetSummary — GET /api/cairn/v1/repos/{owner}/{repo}/pulls/{index}/summary.
func GetSummary(ctx *context.APIContext) {
	repo := resolveRepoForRead(ctx)
	if repo == nil {
		return
	}
	prNumber := ctx.ParamsInt64(":index")
	if prNumber <= 0 {
		ctx.Error(http.StatusBadRequest, "invalid pr number", nil)
		return
	}
	svc := summarizer.Global()
	if svc == nil {
		ctx.Error(http.StatusServiceUnavailable, "simplifier disabled", nil)
		return
	}
	row, err := svc.GetCachedSummary(ctx, repo.ID, prNumber)
	if errors.Is(err, summarizer.ErrNoSummary) {
		ctx.Error(http.StatusNotFound, "no summary", nil)
		return
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "lookup", err)
		return
	}
	ctx.JSON(http.StatusOK, SummaryResponse{
		SummaryMD:   row.SummaryMD,
		ModelID:     row.ModelID,
		GeneratedAt: row.GeneratedUnix,
	})
}

// PostRegenerate — POST /api/cairn/v1/repos/{owner}/{repo}/pulls/{index}/summary/regenerate.
//
// Forces a fresh summary generation. Public repos run with DataScopeFull;
// private repos require enabled SummarizerRepoConsent and use the consent's
// data scope. 503 if the simplifier service is not initialized.
func PostRegenerate(ctx *context.APIContext) {
	if ctx.Doer == nil {
		ctx.Error(http.StatusUnauthorized, "unauthenticated", nil)
		return
	}
	repo := resolveRepoForRead(ctx)
	if repo == nil {
		return
	}
	perm, err := access.GetUserRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetUserRepoPermission", err)
		return
	}
	if !perm.CanWriteIssuesOrPulls(true) && !ctx.Doer.IsAdmin {
		ctx.Error(http.StatusForbidden, "write permission required", nil)
		return
	}

	prNumber := ctx.ParamsInt64(":index")
	if prNumber <= 0 {
		ctx.Error(http.StatusBadRequest, "invalid pr number", nil)
		return
	}

	svc := summarizer.Global()
	if svc == nil {
		ctx.Error(http.StatusServiceUnavailable, "simplifier disabled", nil)
		return
	}

	issue, err := issues_model.GetIssueByIndex(ctx, repo.ID, prNumber)
	if err != nil {
		if issues_model.IsErrIssueNotExist(err) {
			ctx.Error(http.StatusNotFound, "pr not found", nil)
			return
		}
		ctx.Error(http.StatusInternalServerError, "GetIssueByIndex", err)
		return
	}
	if !issue.IsPull {
		ctx.Error(http.StatusBadRequest, "not a pull request", nil)
		return
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		ctx.Error(http.StatusInternalServerError, "LoadPullRequest", err)
		return
	}

	scope := cairnmodels.DataScopeFull
	if repo.IsPrivate {
		consent := &cairnmodels.SummarizerRepoConsent{}
		has, cerr := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Get(consent)
		if cerr != nil {
			ctx.Error(http.StatusInternalServerError, "load consent", cerr)
			return
		}
		if !has || !consent.Enabled {
			ctx.Error(http.StatusBadRequest, "private repo summarization not enabled", nil)
			return
		}
		scope = consent.DataScope
		if !scope.IsValid() {
			scope = cairnmodels.DataScopeMetadata
		}
	}

	prCtx, err := summarizer.BuildPRContextFromForgejo(ctx, repo, issue.PullRequest, issue, scope)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "BuildPRContextFromForgejo", err)
		return
	}

	row, err := svc.RegenerateSummary(ctx, repo.ID, prNumber, repo.OwnerID, prCtx, scope)
	if err != nil {
		if errors.Is(err, summarizer.ErrNotConfigured) {
			ctx.Error(http.StatusServiceUnavailable, "simplifier disabled", nil)
			return
		}
		ctx.Error(http.StatusInternalServerError, "regenerate", err)
		return
	}

	ctx.JSON(http.StatusOK, SummaryResponse{
		SummaryMD:   row.SummaryMD,
		ModelID:     row.ModelID,
		GeneratedAt: row.GeneratedUnix,
	})
}
