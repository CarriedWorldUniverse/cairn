// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"encoding/json"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// TestPolicyToResponse_RoundTrip exercises the response struct through
// JSON marshal/unmarshal, asserting the wire-key contract. The wire
// format `{require_human_only: bool}` is part of Cairn's public API
// surface (see spec §4.5) — clients depend on the exact key. This test
// pins the snake_case key + bool shape so a refactor that renames the
// struct field can't silently break the contract.
func TestPolicyToResponse_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		p    *cairnmodels.ReviewPolicy
		want bool
	}{
		{"nil_policy_zero_value", nil, false},
		{"require_human_only_true", &cairnmodels.ReviewPolicy{OwnerID: 1, RequireHumanOnly: true}, true},
		{"require_human_only_false", &cairnmodels.ReviewPolicy{OwnerID: 1, RequireHumanOnly: false}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := policyToResponse(tc.p)
			if resp.RequireHumanOnly != tc.want {
				t.Errorf("RequireHumanOnly = %v, want %v", resp.RequireHumanOnly, tc.want)
			}
			body, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			// Round-trip into a map to verify the literal wire key.
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			got, ok := m["require_human_only"]
			if !ok {
				t.Fatalf("response missing require_human_only key; got %s", body)
			}
			if got != tc.want {
				t.Errorf("require_human_only = %v, want %v (body=%s)", got, tc.want, body)
			}
			if len(m) != 1 {
				t.Errorf("response has unexpected extra keys: %s", body)
			}
		})
	}
}

// TestReviewPolicyRequest_DecodeShape pins the request wire format
// symmetrically to the response. A body with require_human_only must
// decode into the bool field; unknown extra keys are ignored (Go's
// default json.Decoder behavior — documented here as intentional, so
// adding a stricter decoder later is a deliberate decision).
func TestReviewPolicyRequest_DecodeShape(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"require_human_only":true}`, true},
		{`{"require_human_only":false}`, false},
		{`{"require_human_only":true,"unknown":42}`, true},
	}
	for _, tc := range cases {
		var req ReviewPolicyRequest
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Errorf("Unmarshal(%q): %v", tc.body, err)
			continue
		}
		if req.RequireHumanOnly != tc.want {
			t.Errorf("body=%q: RequireHumanOnly=%v want %v", tc.body, req.RequireHumanOnly, tc.want)
		}
	}
}
