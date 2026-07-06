package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestJWTSignVerify(t *testing.T) {
	v := NewJWTVerifier([]byte("topsecret"))
	tok, err := v.Sign("org-a", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	org, err := v.Verify(tok)
	if err != nil || org != "org-a" {
		t.Fatalf("Verify = %q, %v; want org-a, nil", org, err)
	}
}

func TestJWTRejectsExpired(t *testing.T) {
	v := NewJWTVerifier([]byte("s"))
	tok, _ := v.Sign("org-a", -time.Minute) // already expired
	if _, err := v.Verify(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token err = %v, want ErrInvalidToken", err)
	}
}

func TestJWTRejectsTamperAndWrongSecret(t *testing.T) {
	v := NewJWTVerifier([]byte("right"))
	tok, _ := v.Sign("org-a", time.Hour)

	// Tampered payload (flip a char in the middle segment).
	parts := strings.Split(tok, ".")
	bad := parts[0] + "." + flip(parts[1]) + "." + parts[2]
	if _, err := v.Verify(bad); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("tampered token accepted")
	}

	// Different secret must not verify.
	if _, err := NewJWTVerifier([]byte("wrong")).Verify(tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("token verified under the wrong secret")
	}

	// Garbage shapes.
	for _, junk := range []string{"", "a.b", "a.b.c.d", "not-a-jwt"} {
		if _, err := v.Verify(junk); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("junk %q accepted", junk)
		}
	}
}

func flip(s string) string {
	b := []byte(s)
	i := len(b) / 2
	if b[i] == 'A' {
		b[i] = 'B'
	} else {
		b[i] = 'A'
	}
	return string(b)
}

// TestJWTFlow exercises the full path: API key -> POST /v1/token -> use the JWT.
func TestJWTFlow(t *testing.T) {
	s, key := testServer(t, nil)
	s.UseJWT([]byte("server-secret"))

	// Exchange the API key for a JWT.
	code, body := do(t, s, http.MethodPost, "/v1/token", key, nil)
	if code != http.StatusCreated {
		t.Fatalf("issue token = %d %v", code, body)
	}
	token, _ := body["token"].(string)
	if token == "" || !looksLikeJWT(token) {
		t.Fatalf("bad token: %q", token)
	}

	// The JWT authenticates a normal request (upload) with no API key.
	code, up := do(t, s, http.MethodPost, "/v1/documents", token, textPDF("via jwt"))
	if code != http.StatusCreated {
		t.Fatalf("upload with JWT = %d %v", code, up)
	}

	// A tampered JWT is rejected.
	if code, _ := do(t, s, http.MethodPost, "/v1/documents", token+"x", textPDF("x")); code != http.StatusUnauthorized {
		t.Errorf("tampered JWT: code = %d, want 401", code)
	}
	// The API key still works alongside JWT.
	if code, _ := do(t, s, http.MethodGet, "/v1/documents/"+up["id"].(string), key, nil); code != http.StatusOK {
		t.Errorf("API key alongside JWT: code = %d, want 200", code)
	}
}

func TestTokenEndpointDisabledWithoutJWT(t *testing.T) {
	s, key := testServer(t, nil) // no UseJWT
	if code, _ := do(t, s, http.MethodPost, "/v1/token", key, nil); code != http.StatusNotImplemented {
		t.Fatalf("token without JWT enabled: code = %d, want 501", code)
	}
}
