// Package authheaders validates and applies per-account upstream header rules.
package authheaders

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

const MetadataKey = "request_header_rules"

type Rule struct {
	ID              string     `json:"id,omitempty"`
	Models          []string   `json:"models,omitempty"`
	DurationMinutes int        `json:"duration_minutes,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Name            string     `json:"name"`
	Operation       string     `json:"operation"`
	Value           string     `json:"value,omitempty"`
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
	ids := make(map[string]bool, len(rules))
	for i := range rules {
		r := &rules[i]
		if !httpguts.ValidHeaderFieldName(r.Name) || len(r.Name) > 128 {
			return nil, fmt.Errorf("header rule %d: invalid name", i+1)
		}
		r.Name = http.CanonicalHeaderKey(r.Name)
		if Protected(r.Name) {
			return nil, fmt.Errorf("header rule %d: %s is system-managed", i+1, r.Name)
		}
		if !ValidDuration(r.DurationMinutes) || (r.DurationMinutes == 0) != (r.ExpiresAt == nil) {
			return nil, fmt.Errorf("header rule %d: invalid duration or expiry", i+1)
		}
		if len(r.Models) > 128 {
			return nil, fmt.Errorf("header rule %d: too many models", i+1)
		}
		models := make(map[string]bool)
		for _, model := range r.Models {
			model = strings.TrimSpace(model)
			if model == "" || len(model) > 256 || strings.ContainsAny(model, "*?\r\n") {
				return nil, fmt.Errorf("header rule %d: invalid model", i+1)
			}
			models[model] = true
		}
		r.Models = nil
		for model := range models {
			r.Models = append(r.Models, model)
		}
		sort.Strings(r.Models)
		scopes := r.Models
		if len(scopes) == 0 {
			scopes = []string{""}
		}
		for _, model := range scopes {
			key := r.Name + "\x00" + model
			if seen[key] {
				return nil, fmt.Errorf("header rule %d: duplicate header/model scope", i+1)
			}
			seen[key] = true
		}
		if r.ID == "" {
			identity, _ := json.Marshal([]any{r.Name, r.Models})
			sum := sha256.Sum256(identity)
			r.ID = "legacy-" + hex.EncodeToString(sum[:12])
		}
		if len(r.ID) > 100 || ids[r.ID] {
			return nil, fmt.Errorf("header rule %d: invalid or duplicate ID", i+1)
		}
		ids[r.ID] = true
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

func ValidDuration(minutes int) bool {
	return minutes == 0 || (minutes >= 10 && minutes <= 60 && minutes%10 == 0)
}

// Select resolves model-specific rules before applying them, so a scoped default
// does not accidentally inherit a value from a lower-priority account rule.
func Select(rules []Rule, model string, now time.Time) []Rule {
	selected := make(map[string]Rule)
	for _, r := range rules {
		if r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
			continue
		}
		matches := len(r.Models) == 0
		for _, candidate := range r.Models {
			if candidate == model {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		previous, exists := selected[r.Name]
		if !exists || len(r.Models) > 0 || len(previous.Models) == 0 {
			selected[r.Name] = r
		}
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Rule, 0, len(keys))
	for _, key := range keys {
		out = append(out, selected[key])
	}
	return out
}

// Signature describes only effective mutations, not IDs or countdown metadata.
func Signature(rules []Rule) string {
	if len(rules) == 0 {
		return ""
	}
	values := make([][3]string, 0, len(rules))
	for _, r := range rules {
		values = append(values, [3]string{r.Name, r.Operation, r.Value})
	}
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
