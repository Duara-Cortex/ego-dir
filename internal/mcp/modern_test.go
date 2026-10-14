package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// meta is the _meta of a modern request for version.
func meta(version string) string {
	return fmt.Sprintf(`"_meta":{"io.modelcontextprotocol/protocolVersion":%q,`+
		`"io.modelcontextprotocol/clientInfo":{"name":"scripted","version":"0"},`+
		`"io.modelcontextprotocol/clientCapabilities":{}}`, version)
}

func modern(params string) string {
	if params == "" {
		return "{" + meta(ModernVersions[0]) + "}"
	}
	return "{" + meta(ModernVersions[0]) + "," + params + "}"
}

// modernResult is what every modern result carries.
type modernResult struct {
	ResultType string `json:"resultType"`
	Meta       struct {
		ServerInfo *struct{ Name, Version string } `json:"io.modelcontextprotocol/serverInfo"`
	} `json:"_meta"`
	TTLMs      *int    `json:"ttlMs"`
	CacheScope *string `json:"cacheScope"`
}

func decodeModern(t *testing.T, r reply, cacheable bool) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("error: %s", r.raw)
	}
	var m modernResult
	if err := json.Unmarshal(r.Result, &m); err != nil {
		t.Fatal(err)
	}
	if m.ResultType != "complete" {
		t.Errorf("resultType = %q, want complete: %s", m.ResultType, r.raw)
	}
	if si := m.Meta.ServerInfo; si == nil || si.Name != "test" || si.Version != "v1.2.3" {
		t.Errorf("_meta serverInfo missing or wrong: %s", r.raw)
	}
	if cacheable {
		if m.TTLMs == nil || *m.TTLMs < 0 || m.CacheScope == nil || *m.CacheScope != "public" {
			t.Errorf("caching hints missing: %s", r.raw)
		}
	} else if m.TTLMs != nil || m.CacheScope != nil {
		t.Errorf("caching hints on a result that is not cacheable: %s", r.raw)
	}
}

func TestDiscover(t *testing.T) {
	c := start(t, newServer(echo))
	r := c.call(1, "server/discover", modern(""))
	decodeModern(t, r, true)
	var res struct {
		SupportedVersions []string                   `json:"supportedVersions"`
		Capabilities      map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if want := slices.Concat(ModernVersions, LegacyVersions); !slices.Equal(res.SupportedVersions, want) {
		t.Errorf("supportedVersions = %v, want %v", res.SupportedVersions, want)
	}
	if len(res.Capabilities) != 1 || string(res.Capabilities["tools"]) != "{}" {
		t.Errorf("capabilities = %s", r.Result)
	}
}

// A dual-era client probes with server/discover and falls back to
// initialize on any error that is not a modern one. Without _meta, the
// probe is not a modern request, so it gets a plain error.
func TestDiscoverNeedsMeta(t *testing.T) {
	c := start(t, newServer())
	for id, params := range []string{"", `{}`, `{"_meta":{}}`} {
		wantError(t, c.call(id+1, "server/discover", params), CodeInvalidParams)
	}
}

func TestUnsupportedModernVersion(t *testing.T) {
	var ran atomic.Bool
	tool := echo
	tool.Handler = func(context.Context, json.RawMessage) (string, error) { ran.Store(true); return "", nil }
	c := start(t, newServer(tool))
	for id, method := range map[int]string{1: "server/discover", 2: "tools/list", 3: "tools/call"} {
		params := "{" + meta("1900-01-01") + `,"name":"echo","arguments":{"word":"x"}}`
		r := c.call(id, method, params)
		wantError(t, r, CodeUnsupportedProtocolVersion)
		var e struct {
			Error struct {
				Data struct {
					Supported []string `json:"supported"`
					Requested string   `json:"requested"`
				} `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.raw), &e); err != nil {
			t.Fatal(err)
		}
		if e.Error.Data.Requested != "1900-01-01" || !slices.Equal(e.Error.Data.Supported, slices.Concat(ModernVersions, LegacyVersions)) {
			t.Errorf("%s: data = %+v", method, e.Error.Data)
		}
	}
	// A legacy version named in _meta is not a modern version either.
	wantError(t, c.call(4, "tools/list", "{"+meta(LegacyVersions[0])+"}"), CodeUnsupportedProtocolVersion)
	if ran.Load() {
		t.Error("the tool ran for an unsupported version")
	}
}

func TestModernRequestNeedsCapabilities(t *testing.T) {
	c := start(t, newServer(echo))
	cases := []string{
		fmt.Sprintf(`{"_meta":{"io.modelcontextprotocol/protocolVersion":%q}}`, ModernVersions[0]),
		fmt.Sprintf(`{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientCapabilities":null}}`, ModernVersions[0]),
		fmt.Sprintf(`{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientCapabilities":[]}}`, ModernVersions[0]),
		`{"_meta":{"io.modelcontextprotocol/protocolVersion":20260728,"io.modelcontextprotocol/clientCapabilities":{}}}`,
	}
	for i, params := range cases {
		wantError(t, c.call(i+1, "tools/list", params), CodeInvalidParams)
	}
}

// Modern requests stand alone: no initialize comes first.
func TestModernToolsWithoutHandshake(t *testing.T) {
	c := start(t, newServer(echo))

	r := c.call(1, "tools/list", modern(""))
	decodeModern(t, r, true)
	if !strings.Contains(r.raw, `"name":"echo"`) || !strings.Contains(r.raw, `"readOnlyHint":true`) {
		t.Errorf("tools/list = %s", r.raw)
	}

	r = c.call(2, "tools/call", modern(`"name":"echo","arguments":{"word":"hello"}`))
	decodeModern(t, r, false)
	if res := resultOf(t, r); res.IsError || res.Content[0].Text != "hello" {
		t.Errorf("success = %+v", res)
	}

	r = c.call(3, "tools/call", modern(`"name":"echo","arguments":{}`))
	decodeModern(t, r, false)
	if res := resultOf(t, r); !res.IsError || res.Content[0].Text != "word is required" {
		t.Errorf("tool error = %+v", res)
	}

	wantError(t, c.call(4, "tools/call", modern(`"name":"nope"`)), CodeInvalidParams)
	// ping was removed from the modern protocol.
	wantError(t, c.call(5, "ping", modern("")), CodeMethodNotFound)
	wantError(t, c.call(6, "subscriptions/listen", modern(`"notifications":{"toolsListChanged":true}`)), CodeMethodNotFound)
}

// Legacy results keep their old shape: no resultType, _meta or caching
// hints.
func TestLegacyResultsHaveNoModernFields(t *testing.T) {
	c := start(t, newServer(echo))
	c.call(1, "initialize", fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{}}`, LegacyVersions[0]))
	for id, r := range map[int]reply{
		2: c.call(2, "tools/list", `{}`),
		3: c.call(3, "tools/call", `{"name":"echo","arguments":{"word":"x"},"_meta":{"progressToken":7}}`),
	} {
		for _, field := range []string{"resultType", "_meta", "ttlMs", "cacheScope"} {
			if strings.Contains(string(r.Result), `"`+field+`"`) {
				t.Errorf("request %d: legacy result has %s: %s", id, field, r.raw)
			}
		}
	}
}

// Both eras on one connection: the era belongs to the request.
func TestErasMix(t *testing.T) {
	c := start(t, newServer(echo))
	c.call(1, "initialize", fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{}}`, LegacyVersions[0]))
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	decodeModern(t, c.call(2, "tools/call", modern(`"name":"echo","arguments":{"word":"a"}`)), false)
	if r := c.call(3, "tools/call", `{"name":"echo","arguments":{"word":"b"}}`); strings.Contains(r.raw, "resultType") {
		t.Errorf("legacy call after a modern one: %s", r.raw)
	}
}

// A 2026-07-28 client that sends initialize anyway gets the latest legacy
// version: initialize only negotiates legacy ones.
func TestInitializeNeverAnswersModern(t *testing.T) {
	c := start(t, newServer())
	r := c.call(1, "initialize", fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{}}`, ModernVersions[0]))
	if !strings.Contains(string(r.Result), fmt.Sprintf(`"protocolVersion":%q`, LegacyVersions[0])) {
		t.Errorf("initialize = %s", r.raw)
	}
}

func TestModernCancellation(t *testing.T) {
	b := newBlocker()
	c := start(t, newServer(b.tool()))
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + modern(`"name":"block","arguments":{}`) + `}`)
	waitFor(t, b.started, "the call to start")
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"user"}}`)
	waitFor(t, b.ended, "the call to end")
	if rest := c.close(); len(rest) != 0 {
		t.Errorf("a cancelled call got a reply: %v", rest)
	}
}
