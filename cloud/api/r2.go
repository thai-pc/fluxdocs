package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// r2.go implements a DocumentStore over S3-compatible object storage
// (Cloudflare R2). Rather than pull in a heavy SDK, it speaks the S3 REST API
// directly with AWS Signature Version 4 signing over net/http — matching the
// project's minimal-dependency, hand-written ethos and keeping the whole thing
// testable offline against a mock S3 endpoint.
//
// Objects are keyed "<org_id>/<id>", so every tenant's documents live under
// their own prefix and a Get with the wrong org can never reach another
// tenant's object (.claude/SECURITY.md §4).

// R2Config configures an R2DocumentStore. For Cloudflare R2, Endpoint is
// https://<account>.r2.cloudflarestorage.com and Region is "auto".
type R2Config struct {
	Endpoint  string // scheme + host, no trailing slash
	Bucket    string
	Region    string // "auto" for R2
	AccessKey string
	SecretKey string
}

// R2DocumentStore stores documents as S3 objects.
type R2DocumentStore struct {
	cfg  R2Config
	http *http.Client
}

// NewR2DocumentStore returns a store using cfg. A nil client uses a sane default.
func NewR2DocumentStore(cfg R2Config, client *http.Client) *R2DocumentStore {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	return &R2DocumentStore{cfg: cfg, http: client}
}

func (s *R2DocumentStore) key(orgID, id string) string { return orgID + "/" + id }

// Put uploads the document bytes.
func (s *R2DocumentStore) Put(ctx context.Context, orgID, id string, data []byte) error {
	resp, err := s.do(ctx, http.MethodPut, s.key(orgID, id), data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fluxdocs/api: R2 put: status %d", resp.StatusCode)
	}
	return nil
}

// Get downloads the document bytes, or ErrNotFound on a 404.
func (s *R2DocumentStore) Get(ctx context.Context, orgID, id string) ([]byte, error) {
	resp, err := s.do(ctx, http.MethodGet, s.key(orgID, id), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("fluxdocs/api: R2 get: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// do builds, signs (SigV4), and sends one S3 request for object key.
func (s *R2DocumentStore) do(ctx context.Context, method, key string, body []byte) (*http.Response, error) {
	path := "/" + s.cfg.Bucket + "/" + key
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.Endpoint+encodePath(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	s.sign(req, body)
	return s.http.Do(req)
}

// sign adds the SigV4 Authorization and x-amz-* headers to req (service "s3").
func (s *R2DocumentStore) sign(req *http.Request, body []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("Host", req.URL.Host)

	// Canonical request.
	const signedHeaders = "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		req.Method,
		encodePath(req.URL.Path),
		"", // no query
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign.
	scope := strings.Join([]string{dateStamp, s.cfg.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// Signing key + signature.
	kDate := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, s.cfg.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, signature))
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// encodePath URI-encodes each path segment per AWS canonicalization (unreserved
// characters stay literal; '/' is preserved between segments).
func encodePath(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		segs[i] = awsEncode(seg)
	}
	return strings.Join(segs, "/")
}

func awsEncode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

var _ DocumentStore = (*R2DocumentStore)(nil)
