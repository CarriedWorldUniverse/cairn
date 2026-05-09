// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"encoding/json"
	"net/http"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/reviewpolicy"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

const maxReviewPolicyBody = 256

// ReviewPolicyRequest is the wire format for PUT
// /api/cairn/v1/orgs/{owner}/review-policy.
type ReviewPolicyRequest struct {
	RequireHumanOnly bool `json:"require_human_only"`
}

// ReviewPolicyResponse is the wire format for GET/PUT
// /api/cairn/v1/orgs/{owner}/review-policy.
type ReviewPolicyResponse struct {
	RequireHumanOnly bool `json:"require_human_only"`
}

// resolveOwnerForReviewPolicy loads the owner user named in :owner and
// enforces the org-config auth rule (caller must be that user or a
// site admin). Returns nil after writing an error response on failure.
//
// Mirrors resolveOwnerForSummarizer's posture deliberately: the auth
// boundary for per-org Cairn settings is "site admin OR the org/user
// itself"; do not diverge without an explicit reason.
func resolveOwnerForReviewPolicy(ctx *context.APIContext) *user_model.User {
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

// policyToResponse renders a ReviewPolicy as the public response. Pure
// helper — pulled out so the response shape can be unit-tested without
// spinning up an APIContext.
func policyToResponse(p *cairnmodels.ReviewPolicy) ReviewPolicyResponse {
	if p == nil {
		return ReviewPolicyResponse{}
	}
	return ReviewPolicyResponse{RequireHumanOnly: p.RequireHumanOnly}
}

// GetReviewPolicy — GET /api/cairn/v1/orgs/{owner}/review-policy.
//
// Returns the per-org policy (or the default RequireHumanOnly: true
// when no row exists — this matches Service.Load's fail-safe default).
// 503 if the review-policy service was not initialized at startup.
func GetReviewPolicy(ctx *context.APIContext) {
	owner := resolveOwnerForReviewPolicy(ctx)
	if owner == nil {
		return
	}
	svc := reviewpolicy.Global()
	if svc == nil {
		ctx.Error(http.StatusServiceUnavailable, "review policy disabled", nil)
		return
	}
	p, err := svc.Load(ctx, owner.ID)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load policy", err)
		return
	}
	ctx.JSON(http.StatusOK, policyToResponse(p))
}

// PutReviewPolicy — PUT /api/cairn/v1/orgs/{owner}/review-policy.
//
// Upserts the per-org policy. Body and response are both
// {require_human_only: bool}. 503 if the review-policy service was not
// initialized at startup.
func PutReviewPolicy(ctx *context.APIContext) {
	owner := resolveOwnerForReviewPolicy(ctx)
	if owner == nil {
		return
	}
	if reviewpolicy.Global() == nil {
		ctx.Error(http.StatusServiceUnavailable, "review policy disabled", nil)
		return
	}

	var req ReviewPolicyRequest
	ctx.Req.Body = http.MaxBytesReader(ctx.Resp, ctx.Req.Body, maxReviewPolicyBody)
	if err := json.NewDecoder(ctx.Req.Body).Decode(&req); err != nil {
		ctx.Error(http.StatusBadRequest, "decode body", err)
		return
	}

	p := &cairnmodels.ReviewPolicy{
		OwnerID:          owner.ID,
		RequireHumanOnly: req.RequireHumanOnly,
	}

	existing := &cairnmodels.ReviewPolicy{}
	has, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Get(existing)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load existing", err)
		return
	}
	if has {
		// AllCols ensures the zero-valued bool (RequireHumanOnly=false)
		// actually writes through xorm's update path — same reason
		// summarizer.go uses AllCols on its config update.
		if _, err := db.GetEngine(ctx).ID(owner.ID).AllCols().Update(p); err != nil {
			ctx.Error(http.StatusInternalServerError, "update", err)
			return
		}
	} else {
		if _, err := db.GetEngine(ctx).Insert(p); err != nil {
			ctx.Error(http.StatusInternalServerError, "insert", err)
			return
		}
	}

	ctx.JSON(http.StatusOK, policyToResponse(p))
}
