package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// listTools drives tools/list through the server's stdio loop and returns the
// advertised tool names.
func listTools(t *testing.T, serve func(r *strings.Reader, w *bytes.Buffer) error) []string {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	if err := serve(in, &out); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode tools/list: %v (%s)", err, out.String())
	}
	names := make([]string, 0, len(resp.Result.Tools))
	for _, tl := range resp.Result.Tools {
		if strings.Contains(tl.Description, "leaves this machine") {
			t.Errorf("tool %q is described as sending document content out", tl.Name)
		}
		names = append(names, tl.Name)
	}
	return names
}

// TestDefaultBuildHasNoNetworkTools pins the privacy promise for the root
// binary: even with an API key and the opt-in env var set, no tool that sends
// document content out is registered.
func TestDefaultBuildHasNoNetworkTools(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test-not-a-real-key", "FLUXDOCS_MCP_ENABLE_AI": "1"}
	var stderr bytes.Buffer
	s, err := newServer(nil, func(k string) string { return env[k] }, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	names := listTools(t, func(r *strings.Reader, w *bytes.Buffer) error { return s.Serve(r, w) })
	if len(names) == 0 {
		t.Fatal("no tools listed")
	}
	for _, n := range names {
		if n == "extract_structured" || n == "ask_document" {
			t.Errorf("default build registers network-sending tool %q", n)
		}
	}
	if strings.Contains(stderr.String(), "sk-ant") {
		t.Error("stderr leaks the API key")
	}
}

func TestEnableFlagRejected(t *testing.T) {
	_, err := newServer([]string{"--enable-cloud-ai"}, func(string) string { return "" }, &bytes.Buffer{})
	if !errors.Is(err, errNoAIBackend) {
		t.Fatalf("err = %v, want errNoAIBackend", err)
	}
}
