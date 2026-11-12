package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
)

// stubAI is an offline core.AIClient so the opt-in path needs no network.
type stubAI struct{}

func (stubAI) Extract(context.Context, string, core.Schema) (map[string]any, error) {
	return map[string]any{}, nil
}
func (stubAI) Ask(context.Context, string, string) (string, error) { return "", nil }

const fakeKey = "sk-ant-test-not-a-real-key"

// listTools runs tools/list over the stdio loop and returns name→description.
func listTools(t *testing.T, args []string, env map[string]string, backend core.AIClient) (map[string]string, string) {
	t.Helper()
	var stderr bytes.Buffer
	s, err := newServer(args, func(k string) string { return env[k] }, &stderr, backend)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			Tools []struct{ Name, Description string } `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, tl := range resp.Result.Tools {
		m[tl.Name] = tl.Description
	}
	return m, stderr.String()
}

var aiToolNames = []string{"extract_structured", "ask_document"}

// TestAIOffByDefault: an API key alone must not register any tool that sends
// document content out.
func TestAIOffByDefault(t *testing.T) {
	tools, _ := listTools(t, nil, map[string]string{"ANTHROPIC_API_KEY": fakeKey}, nil)
	if _, ok := tools["open_document"]; !ok {
		t.Fatal("core tools missing")
	}
	for _, n := range aiToolNames {
		if _, ok := tools[n]; ok {
			t.Errorf("%s registered without opt-in", n)
		}
	}
	// An explicit false is also off.
	tools, _ = listTools(t, nil, map[string]string{"ANTHROPIC_API_KEY": fakeKey, "FLUXDOCS_MCP_ENABLE_AI": "0"}, nil)
	for _, n := range aiToolNames {
		if _, ok := tools[n]; ok {
			t.Errorf("%s registered with FLUXDOCS_MCP_ENABLE_AI=0", n)
		}
	}
}

func TestAIOptIn(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"flag", []string{"--enable-cloud-ai"}, map[string]string{"ANTHROPIC_API_KEY": fakeKey}},
		{"env", nil, map[string]string{"ANTHROPIC_API_KEY": fakeKey, "FLUXDOCS_MCP_ENABLE_AI": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools, stderr := listTools(t, tc.args, tc.env, nil)
			for _, n := range aiToolNames {
				desc, ok := tools[n]
				if !ok {
					t.Errorf("%s not registered on opt-in", n)
					continue
				}
				if !strings.Contains(desc, provider) {
					t.Errorf("%s description does not name %s: %q", n, provider, desc)
				}
			}
			if strings.Contains(stderr, fakeKey) {
				t.Error("stderr leaks the API key")
			}
		})
	}
	// Injected backend (no key needed).
	tools, _ := listTools(t, []string{"--enable-cloud-ai"}, nil, stubAI{})
	if _, ok := tools["ask_document"]; !ok {
		t.Error("ask_document not registered with an injected backend")
	}
}

func TestAIOptInErrors(t *testing.T) {
	_, err := newServer([]string{"--enable-cloud-ai"}, func(string) string { return "" }, &bytes.Buffer{}, nil)
	if !errors.Is(err, errNoKey) {
		t.Errorf("no key: err = %v, want errNoKey", err)
	}
	env := map[string]string{"FLUXDOCS_MCP_ENABLE_AI": "maybe"}
	if _, err := newServer(nil, func(k string) string { return env[k] }, &bytes.Buffer{}, nil); err == nil {
		t.Error("bad FLUXDOCS_MCP_ENABLE_AI accepted")
	}
}
