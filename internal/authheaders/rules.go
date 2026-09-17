// Package authheaders validates and applies per-account upstream header rules.
package authheaders

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const MetadataKey = "request_header_rules"

type Rule struct {
	Name      string `json:"name"`
	Operation string `json:"operation"`
	Value     string `json:"value,omitempty"`
}

// Decode accepts JSON metadata without exposing header values in errors.
func Decode(value any) ([]Rule, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid %s", MetadataKey)
	}
	var rules []Rule
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&rules); err != nil {
		return nil, fmt.Errorf("%s must be an array of header rules", MetadataKey)
	}
	if len(rules) > 32 {
		return nil, fmt.Errorf("%s supports at most 32 rules", MetadataKey)
	}
	seen := make(map[string]bool, len(rules))
	for i := range rules {
		r := &rules[i]
		if !httpguts.ValidHeaderFieldName(r.Name) || len(r.Name) > 128 {
			return nil, fmt.Errorf("header rule %d: invalid name", i+1)
		}
		r.Name = http.CanonicalHeaderKey(r.Name)
		if Protected(r.Name) {
			return nil, fmt.Errorf("header rule %d: %s is system-managed", i+1, r.Name)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("header rule %d: duplicate name", i+1)
		}
		seen[r.Name] = true
		switch r.Operation {
		case "default", "override":
			if len(r.Value) > 8192 || !httpguts.ValidHeaderFieldValue(r.Value) {
				return nil, fmt.Errorf("header rule %d: invalid value", i+1)
			}
		case "delete":
			r.Value = ""
		default:
			return nil, fmt.Errorf("header rule %d: invalid operation", i+1)
		}
	}
	return rules, nil
}

// Protected keeps credentials, transport framing and session identity under system control.
func Protected(name string) bool {
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "sec-websocket-") || strings.HasPrefix(name, "proxy-") {
		return true
	}
	switch name {
	case "authorization", "x-api-key", "api-key", "cookie", "set-cookie", "host",
		"connection", "upgrade", "content-length", "transfer-encoding", "te", "trailer",
		"keep-alive", "expect", "content-type", "content-encoding", "accept-encoding", "accept", "openai-beta",
		"chatgpt-account-id", "openai-organization", "openai-project",
		"session-id", "session_id", "conversation-id", "conversation_id", "thread-id", "thread_id",
		"x-codex-window-id", "x-codex-device-id", "x-codex-installation-id", "oai-device-id", "x-device-id",
		"x-codex-routing-hint", "x-codex-turn-state", "x-codex-turn-metadata", "x-client-request-id":
		return true
	}
	return false
}

func Apply(headers http.Header, rules []Rule) {
	if headers == nil {
		return
	}
	for _, rule := range rules {
		switch rule.Operation {
		case "delete":
			headers.Del(rule.Name)
			if rule.Name == "User-Agent" {
				// An absent User-Agent is replaced by Go's transport default.
				headers.Set(rule.Name, "")
			}
		case "override":
			headers.Set(rule.Name, rule.Value)
		case "default":
			if _, exists := headers[rule.Name]; !exists {
				headers.Set(rule.Name, rule.Value)
			}
		}
	}
}
