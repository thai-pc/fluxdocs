package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// jwt.go implements minimal HS256 JSON Web Tokens over the standard library — no
// third-party dependency, matching the project's minimal-dependency ethos. A
// token authenticates a request the same way an API key does: it resolves to an
// org. Tokens are short-lived and minted by POST /v1/token (itself authenticated
// by an API key), giving callers a bearer credential they can use without
// resending the API key.

// ErrInvalidToken is returned for any malformed, mis-signed, or expired JWT.
var ErrInvalidToken = errors.New("fluxdocs/api: invalid token")

// JWTVerifier signs and verifies HS256 tokens with a shared secret.
type JWTVerifier struct{ secret []byte }

// NewJWTVerifier returns a verifier over secret.
func NewJWTVerifier(secret []byte) *JWTVerifier { return &JWTVerifier{secret: secret} }

type jwtClaims struct {
	OrgID string `json:"org_id"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
}

var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// Sign mints a token for orgID valid for ttl.
func (v *JWTVerifier) Sign(orgID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwtClaims{OrgID: orgID, Iat: now.Unix(), Exp: now.Add(ttl).Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	sig := v.mac(signingInput)
	return signingInput + "." + sig, nil
}

// Verify checks a token's signature and expiry and returns its org.
func (v *JWTVerifier) Verify(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != jwtHeader {
		return "", ErrInvalidToken
	}
	expected := v.mac(parts[0] + "." + parts[1])
	// Constant-time comparison so a wrong signature can't be timed out.
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return "", ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", ErrInvalidToken
	}
	if claims.OrgID == "" || claims.Exp == 0 || time.Now().Unix() >= claims.Exp {
		return "", ErrInvalidToken
	}
	return claims.OrgID, nil
}

func (v *JWTVerifier) mac(signingInput string) string {
	h := hmac.New(sha256.New, v.secret)
	h.Write([]byte(signingInput))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// looksLikeJWT reports whether a bearer token has the three-segment JWT shape,
// so authenticate can tell a JWT from an opaque API key.
func looksLikeJWT(token string) bool {
	return strings.Count(token, ".") == 2
}

// issueToken (POST /v1/token) mints a short-lived JWT for the caller's org. The
// caller is already authenticated (by API key) via the /v1 middleware.
func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) {
	if s.jwt == nil {
		writeError(w, http.StatusNotImplemented, "token auth is not enabled")
		return
	}
	const ttl = time.Hour
	token, err := s.jwt.Sign(orgID(r.Context()), ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"expires_in": int(ttl.Seconds()),
	})
}
