// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/hostmatcher"
	"forgejo.org/modules/json"
	"forgejo.org/modules/jwtx"
	"forgejo.org/modules/log"
	"forgejo.org/modules/proxy"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/services/auth"

	"github.com/gobwas/glob"
	"github.com/golang-jwt/jwt/v5"
)

var (
	_ auth.Method = &AuthorizedIntegration{}

	aiHTTPClient   *http.Client
	initHTTPClient sync.Once

	errParseInternalServer = errors.New("internal server error")

	// Allow mocking / overridding during tests:
	GetAuthorizedIntegrationHTTPClient = func() *http.Client {
		initHTTPClient.Do(initAuthorizedIntegrationHTTPClient)
		return aiHTTPClient
	}
)

// Restrict document size to prevent resource exhaustion attack with a malicious authorized integration; largest
// real-world openid-configuration observed is about 1kB, largest JWKS is 6kB, so for both cases 16kB should be
// sufficient. If this needs to change in the future, it could be moved to a config setting -- but until a reason comes
// up it seems reasonable to keep microscopic settings out-of-sight.
const authorizedIntegrationRequestBodyLimit = int64(16 * 1024)

// Authenticates incoming requests by JWTs that are issued by an authorized integration.  Authorized integrations are
// stored in the database in the [auth_model.AuthorizedIntegration] table.  Once authenticated, the request can perform
// actions as the owner of the authorized integration, with limited access defined by the scope and resources stored on
// the database record.
//
// Authorization is received from HTTP requests as a `Authorization: Bearer [...jwt...]` (or `Authorization: Token
// [...jwt...]`).
type AuthorizedIntegration struct {
	// Permit the use of `Authorization: Basic ...`, in addition to the typical bearer/token authorization header.  If
	// true, the basic password will be interpreted as a JWT token if present and valid.  The username is ignored.
	PermitBasic bool

	// For testing -- interpret JWTs with now as a fixed time.
	fixedTime *time.Time
}

func (a *AuthorizedIntegration) Verify(req *http.Request, w http.ResponseWriter, _ auth.SessionStore) auth.MethodOutput {
	hasToken, token := tokenFromAuthorizationBearer(req).Get()
	if !hasToken {
		if !a.PermitBasic {
			return &auth.AuthenticationNotAttempted{}
		}
		hasBasic, basicToken := tokenFromAuthorizationBasic(req).Get()
		if !hasBasic {
			return &auth.AuthenticationNotAttempted{}
		}
		token = basicToken
	}

	var authorizedIntegration *auth_model.AuthorizedIntegration

	parsedToken, err := jwt.ParseWithClaims(token, &flexibleClaims{},
		func(t *jwt.Token) (any, error) {
			keyID, ok := t.Header["kid"]
			if !ok {
				return nil, errors.New("failed finding key identifer (kid) in JWT headers")
			}

			issuer, err := t.Claims.GetIssuer()
			if err != nil {
				return nil, fmt.Errorf("failed getting `iss` claim: %w", err)
			} else if len(issuer) == 0 {
				return nil, fmt.Errorf("invalid `iss` claim: %q", issuer)
			}
			audienceArray, err := t.Claims.GetAudience()
			if err != nil {
				return nil, fmt.Errorf("failed getting `aud` claim: %w", err)
			} else if len(audienceArray) != 1 {
				return nil, fmt.Errorf("required one and only one `aud` claim, but received %d", len(audienceArray))
			}
			audience := audienceArray[0]
			if len(audience) == 0 {
				return nil, fmt.Errorf("invalid `aud` claim: %q", audience)
			}

			authorizedIntegration, err = auth_model.GetAuthorizedIntegration(req.Context(), issuer, audience)
			if errors.Is(err, util.ErrNotExist) {
				return nil, errors.New("matching authorized_integration not found")
			} else if err != nil {
				return nil, fmt.Errorf("failure reading authorized_integration: %w (%w)", err, errParseInternalServer)
			}

			// Do the claim check before accessing the issuer's OIDC metadata and JWKS, to reduce risk of resource
			// utilization attack through invalid JWTs causing remote requests.
			err = a.checkClaims(t.Claims.(*flexibleClaims), authorizedIntegration.ClaimRules)
			if err != nil {
				return nil, fmt.Errorf("claim mismatch: %w", err)
			}

			issuerURL, err := url.Parse(issuer)
			if err != nil {
				return nil, fmt.Errorf("failed parsing issuer: %w", err)
			}

			issuerOIDCURL := issuerURL.JoinPath(".well-known/openid-configuration")
			var oidcConfig openIDConfiguration
			// TODO: cache external OIDC configuration, with a fixed timeout (not LRU/MRU)
			if err := a.fetchJSON(issuerOIDCURL.String(), &oidcConfig); err != nil {
				return nil, fmt.Errorf("error when fetching .well-known/openid-configuration from %s: %w", issuerOIDCURL, err)
			}

			if oidcConfig.Issuer != issuer {
				return nil, fmt.Errorf("issuer mismatch; expected %q, received %q from %s", issuer, oidcConfig.Issuer, issuerOIDCURL)
			}
			if !slices.Contains(oidcConfig.IDTokenSigningAlgValuesSupported, t.Method.Alg()) {
				return nil, fmt.Errorf("issuer supports signature algorithms %#v, but received token with algorithm %s", oidcConfig.IDTokenSigningAlgValuesSupported, t.Method.Alg())
			}

			jwksURI, err := url.Parse(oidcConfig.JwksURI)
			if err != nil {
				return nil, fmt.Errorf("failed parsing jwks_uri: %w", err)
			} else if jwksURI.Host != issuerURL.Host {
				// Prevent SSRF which could occur if a malicious openid-connection response returned a jwks_uri field
				// that causes Forgejo to access other hostnames.  This could be considered a valid case as well and we
				// can rely on the config-based allowed and blocked domains for the [authorized_integration] section,
				// but until a real-world case comes up where that is needed, this is a safety-first restriction.
				return nil, fmt.Errorf("jwks_uri host mismatch: must be the same as issuer host %q, but was %q", issuerURL.Host, jwksURI.Host)
			}
			var keys openIDKeys
			// TODO: cache JWKS, with a fixed timeout (not LRU/MRU)
			if err := a.fetchJSON(oidcConfig.JwksURI, &keys); err != nil {
				return nil, fmt.Errorf("error when fetching JWKS from %s: %w", oidcConfig.JwksURI, err)
			}

			for _, key := range keys.Keys {
				if key["kid"] == keyID {
					alg, algPresent := key["alg"] // "alg" is an optional field
					if algPresent && alg != t.Method.Alg() {
						return nil, fmt.Errorf("kid %q doesn't match expected algorithm %s, was %v", keyID, t.Method.Alg(), key["alg"])
					}

					use, usePresent := key["use"] // "use" is also an optional field
					if usePresent && use != "sig" {
						return nil, fmt.Errorf("kid %q isn't designated for signing usage, was %s", keyID, key["use"])
					}

					pub, err := jwtx.ParseJWKToPublicKey(key)
					if err != nil {
						return nil, fmt.Errorf("failed to parse JWKS: %w", err)
					}
					return pub, nil
				}
			}

			return nil, errors.New("no key identified")
		},
		jwt.WithValidMethods(jwtx.ValidAsymmetricAlgorithms), // only asymetric algorithms, as JWKS must have a public key only
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(func() time.Time {
			if a.fixedTime != nil {
				return *a.fixedTime
			}
			return time.Now()
		}),
	)
	if err != nil && errors.Is(err, errParseInternalServer) {
		// Errors from parsing marked errParseInternalServer are AuthenticationError, not incorrect creds:
		return &auth.AuthenticationError{Error: err}
	} else if err != nil {
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: fmt.Errorf("authorized integration: parse JWT error: %w", err)}
	} else if !parsedToken.Valid {
		return &auth.AuthenticationAttemptedIncorrectCredential{Error: errors.New("authorized integration: JWT not valid")}
	} else if authorizedIntegration == nil { // shouldn't be possible, but overly safe
		return &auth.AuthenticationError{Error: errors.New("authorized integration: nil authorized integration")}
	}

	u, err := user_model.GetUserByID(req.Context(), authorizedIntegration.UserID)
	if err != nil {
		return &auth.AuthenticationError{Error: fmt.Errorf("authorized integration: GetUserByID: %w", err)}
	}

	if err = authorizedIntegration.UpdateLastUsed(req.Context()); err != nil {
		log.Error("UpdateLastUsed:  %v", err)
	}

	return &auth.AuthenticationSuccess{
		Result: &authorizedIntegrationAuthenticationResult{
			user:  u,
			scope: authorizedIntegration.Scope,
			// TODO: add repo-specific access with an authz reducer
		},
	}
}

func initAuthorizedIntegrationHTTPClient() {
	blockList := hostmatcher.ParseSimpleMatchList("authorized_integration.BLOCKED_DOMAINS", setting.AuthorizedIntegration.BlockedDomains)

	allowList := hostmatcher.ParseSimpleMatchList("authorized_integration.ALLOWED_DOMAINS", setting.AuthorizedIntegration.AllowedDomains)
	if allowList.IsEmpty() {
		// the default policy is that authorized integrations can access external hosts
		allowList.AppendBuiltin(hostmatcher.MatchBuiltinExternal)
	}
	if setting.AuthorizedIntegration.AllowLocalNetworks {
		allowList.AppendBuiltin(hostmatcher.MatchBuiltinPrivate)
		allowList.AppendBuiltin(hostmatcher.MatchBuiltinLoopback)
	}

	aiHTTPClient = &http.Client{
		Timeout: setting.AuthorizedIntegration.RequestTimeout,
		Transport: &http.Transport{
			Proxy:       proxy.Proxy(),
			DialContext: hostmatcher.NewDialContext("authorized_integration", allowList, blockList, setting.Proxy.ProxyURLFixed),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// It might be possible to come up with some reasonable capability to support redirects -- such as
			// keeping them within the same issuer host? -- but there are risks that this can be used for SSRF
			// attacks.  In the face of those risks, and with a lack of real-world use-cases, disable redirects.
			return errors.New("authorized integration: HTTP redirects are disabled")
		},
	}
}

func (a *AuthorizedIntegration) fetchJSON(urlString string, v any) error {
	parsedURL, err := url.Parse(urlString)
	if err != nil {
		return fmt.Errorf("failed parsing URL %q: %w", urlString, err)
	}
	// Fetching openid-connect or JWKS needs to come from a source that is authentic, and therefore only `https` is
	// supported.  This also protects against a trusted issuer being configured maliciously  as `file://` or a JKWS URI
	// being `file://` -- the HTTP client won't permit that, but, extra safety doesn't hurt.
	if parsedURL.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %q", parsedURL.String())
	}

	resp, err := GetAuthorizedIntegrationHTTPClient().Get(parsedURL.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("non-OK response code: %s", resp.Status)
	}

	body := io.LimitReader(resp.Body, authorizedIntegrationRequestBodyLimit)
	decoder := json.NewDecoder(body)
	err = decoder.Decode(&v)
	if err != nil {
		// If a decoding error is hit, decorate with information about the limited body size so that it doesn't look
		// like the remote server provided an incomplete response. err should be something like `io.UnexpectedEOF` in
		// this case, but it actually isn't, so don't bother trying to detect precisely.
		return fmt.Errorf("failed to decode (response body restricted to %d bytes): %w", authorizedIntegrationRequestBodyLimit, err)
	}
	return nil
}

// Compare a map[string]any of incoming claims against an array of claim rules.  All rules must match successfully or
// else an error with the mismatch detail is returned.
func (a *AuthorizedIntegration) checkClaims(incomingClaims any, stored *auth_model.ClaimRules) error {
	if stored == nil {
		return nil
	}

	for _, rule := range stored.Rules {
		var lhs any

		if lhsClaim, isFlex := incomingClaims.(*flexibleClaims); isFlex {
			switch rule.Claim {
			case "iss":
				lhs = lhsClaim.Issuer
			case "sub":
				lhs = lhsClaim.Subject
			case "jti":
				lhs = lhsClaim.ID
			case "aud":
				audienceArray, err := lhsClaim.GetAudience()
				if err != nil {
					return fmt.Errorf("failed getting `aud` claim: %w", err)
				} else if len(audienceArray) != 1 {
					return fmt.Errorf("required one and only one `aud` claim, but received %d", len(audienceArray))
				}
				lhs = audienceArray[0]
			default:
				v, present := lhsClaim.other[rule.Claim]
				if !present {
					return fmt.Errorf("claim rule on %q couldn't be satisfied: claim not found", rule.Claim)
				}
				lhs = v
			}
		} else if lhsMap, isMap := incomingClaims.(map[string]any); isMap {
			v, present := lhsMap[rule.Claim]
			if !present {
				return fmt.Errorf("claim rule on %q couldn't be satisfied: claim not found", rule.Claim)
			}
			lhs = v
		} else {
			return fmt.Errorf("unexpected incoming claims type: %T", incomingClaims)
		}

		switch rule.Comparison {
		case auth_model.ClaimEqual:
			lhsStr, ok := lhs.(string)
			if !ok {
				return fmt.Errorf("claim %q must be a string, but was %T", rule.Claim, lhs)
			} else if lhsStr != rule.Value {
				return fmt.Errorf("claim %q must be %q, but was %q", rule.Claim, rule.Value, lhsStr)
			}
		case auth_model.ClaimGlob:
			lhsStr, ok := lhs.(string)
			if !ok {
				return fmt.Errorf("claim %q must be a string, but was %T", rule.Claim, lhs)
			}
			r, err := glob.Compile(rule.Value)
			if err != nil {
				return fmt.Errorf("unable to parse glob for claim rule on %q; glob = %q, err = %w", rule.Claim, rule.Value, err)
			}
			if !r.Match(lhsStr) {
				return fmt.Errorf("claim %q must match glob %q, but value %q did not match", rule.Claim, rule.Value, lhsStr)
			}
		case auth_model.ClaimNested:
			lhsMap, ok := lhs.(map[string]any)
			if !ok {
				return fmt.Errorf("claim %q must be a map, but was %T", rule.Claim, lhs)
			} else if err := a.checkClaims(lhsMap, rule.Nested); err != nil {
				return fmt.Errorf("in nested claim %q: %w", rule.Claim, err)
			}
		}
	}

	return nil
}
