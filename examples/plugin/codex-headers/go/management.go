package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	_ "modernc.org/sqlite"
)

const settingsPath = "/v0/management/plugins/codex-headers/turn-state"
const rulesPath = "/v0/management/plugins/codex-headers/rules"

func managementRegistration() any {
	return struct {
		Routes []struct {
			Method string `json:"Method"`
			Path   string `json:"Path"`
		} `json:"routes"`
	}{
		Routes: []struct {
			Method string `json:"Method"`
			Path   string `json:"Path"`
		}{
			{Method: http.MethodGet, Path: settingsPath},
			{Method: http.MethodPut, Path: settingsPath},
			{Method: http.MethodGet, Path: rulesPath},
		},
	}
}

func (p *headerPlugin) management(raw []byte) (pluginapi.ManagementResponse, error) {
	var req pluginapi.ManagementRequest
	if json.Unmarshal(raw, &req) != nil {
		return pluginapi.ManagementResponse{}, errors.New("invalid management request")
	}
	if req.Path != settingsPath && req.Path != rulesPath {
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.manager == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "turn state storage unavailable"})
	}
	if req.Path == rulesPath {
		if req.Method != http.MethodGet {
			return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
		views, err := p.ruleViews()
		if err != nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "turn state storage unavailable"})
		}
		return jsonResponse(http.StatusOK, struct {
			Rules []automaticRuleView `json:"rules"`
		}{Rules: views})
	}
	switch req.Method {
	case http.MethodGet:
		return jsonResponse(http.StatusOK, p.manager.Status())
	case http.MethodPut:
		// The existing management API requires enabled and max_chars and preserves
		// omitted scope, allowlist and lifetime fields.
		var input struct {
			Enabled         *bool     `json:"enabled"`
			MaxChars        *int      `json:"max_chars"`
			LifetimeSeconds *int      `json:"lifetime_seconds"`
			AccountScope    *string   `json:"account_scope"`
			AuthIDs         *[]string `json:"auth_ids"`
		}
		if len(req.Body) > 3<<20 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid settings"})
		}
		decoder := json.NewDecoder(bytes.NewReader(req.Body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || input.Enabled == nil || input.MaxChars == nil || *input.MaxChars < 1 || *input.MaxChars > turnstate.MaxChars {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "enabled and integer max_chars (1..8192) are required"})
		}
		if decoder.Decode(new(any)) != io.EOF {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "expected one JSON object"})
		}
		if input.AccountScope != nil && *input.AccountScope != "all" && *input.AccountScope != "selected" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account_scope must be all or selected"})
		}
		if err := p.manager.PatchSettings(*input.Enabled, *input.MaxChars, input.AccountScope, input.AuthIDs, input.LifetimeSeconds); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid settings or storage unavailable"})
		}
		return jsonResponse(http.StatusOK, p.manager.Status())
	default:
		return jsonResponse(http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

type automaticRuleView struct {
	AuthID          string    `json:"auth_id"`
	ID              string    `json:"id"`
	Model           string    `json:"model"`
	Value           string    `json:"value"`
	IssuedAt        time.Time `json:"issued_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Active          bool      `json:"active"`
	AccountEnabled  bool      `json:"account_enabled"`
	DurationMinutes int       `json:"duration_minutes"`
}

// Only the management list enumerates the SQLite account IDs; the request path
// reads rules from the Manager's in-memory snapshot.
func (p *headerPlugin) ruleViews() ([]automaticRuleView, error) {
	result := make([]automaticRuleView, 0)
	db, err := sql.Open("sqlite", p.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT DISTINCT auth_id FROM rules ORDER BY auth_id")
	if err != nil {
		return nil, err
	}
	var accounts []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		accounts = append(accounts, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	minutes := p.manager.Status().LifetimeSeconds / 60
	for _, id := range accounts {
		for _, rule := range p.manager.Rules(id) {
			_, active := p.manager.Lookup(id, rule.Model)
			result = append(result, automaticRuleView{
				AuthID: rule.AuthID, ID: rule.ID(), Model: rule.Model,
				Value: rule.Value, IssuedAt: rule.IssuedAt, ExpiresAt: rule.ExpiresAt,
				Active: active, AccountEnabled: p.manager.AccountEnabled(id),
				DurationMinutes: minutes,
			})
		}
	}
	return result, nil
}

func jsonResponse(code int, value any) (pluginapi.ManagementResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return pluginapi.ManagementResponse{}, err
	}
	return pluginapi.ManagementResponse{
		StatusCode: code,
		Headers:    http.Header{"Content-Type": []string{"application/json"}, "Cache-Control": []string{"no-store"}},
		Body:       body,
	}, nil
}
