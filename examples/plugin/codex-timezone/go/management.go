package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const apiPath = "/v0/management/plugins/codex-timezone/settings"
const refreshPath = "/v0/management/plugins/codex-timezone/refresh"

//go:embed panel.html
var panel []byte

func registration() any {
	return map[string]any{
		"schema_version": 6,
		"metadata":       pluginapi.Metadata{Name: pluginID, Version: version, Author: "GAMPA228", GitHubRepository: "https://github.com/GAMPA228/cpa", ConfigFields: []pluginapi.ConfigField{{Name: "data_file", Type: pluginapi.ConfigFieldTypeString, Description: "Plugin settings file (default: plugins/data/codex-timezone.json)"}}},
		"capabilities":   map[string]bool{"request_interceptor": true, "management_api": true},
	}
}

func (p *timezonePlugin) handle(method string, raw []byte) ([]byte, error) {
	if method == "request.intercept_after" || method == "management.handle" {
		p.touch()
	}
	var result any
	var err error
	switch method {
	case "plugin.register", "plugin.reconfigure":
		err = p.configure(raw)
		result = registration()
	case "plugin.quiesce", "plugin.shutdown":
		p.close()
		result = struct{}{}
	case "request.intercept_before":
		result = pluginapi.RequestInterceptResponse{}
	case "request.intercept_after":
		var req pluginapi.RequestInterceptRequest
		if err = json.Unmarshal(raw, &req); err == nil {
			result = p.intercept(req)
		}
	case "management.register":
		result = map[string]any{
			"resources": []map[string]string{{"Path": "/panel", "Menu": "请求时区"}},
			"routes":    []map[string]string{{"Method": "GET", "Path": apiPath}, {"Method": "PUT", "Path": apiPath}, {"Method": "POST", "Path": refreshPath}},
		}
	case "management.handle":
		var req pluginapi.ManagementRequest
		if err = json.Unmarshal(raw, &req); err == nil {
			result = p.management(req)
		}
	default:
		err = errors.New("unsupported method")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"ok": true, "result": result})
}

func jsonResponse(code int, value any) pluginapi.ManagementResponse {
	body, _ := json.Marshal(value)
	return pluginapi.ManagementResponse{StatusCode: code, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: body}
}

func (p *timezonePlugin) snapshot() any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	type row struct {
		accountView
		Observation observation `json:"observation"`
		Effective   rule        `json:"effective"`
	}
	rows := make([]row, 0, len(p.accounts))
	for id, a := range p.accounts {
		rows = append(rows, row{a, p.cache[a.key], effectiveRule(p.settings, id)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	// Marshal while locked: the returned view must not retain mutable maps.
	raw, _ := json.Marshal(map[string]any{"settings": p.settings, "accounts": rows, "refreshing": p.refreshing, "error": p.status, "version": version})
	return json.RawMessage(raw)
}

func (p *timezonePlugin) management(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	// Only static HTML is public. All account data and writes use host-authenticated routes.
	if req.Method == "GET" && (req.Path == "/panel" || strings.HasSuffix(req.Path, "/codex-timezone/panel")) {
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "Referrer-Policy": {"no-referrer"}, "X-Content-Type-Options": {"nosniff"}, "Content-Security-Policy": {"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'self'; frame-ancestors 'self'"}}, Body: panel}
	}
	if req.Path == refreshPath && req.Method == "POST" {
		p.requestRefresh(true)
		return jsonResponse(202, map[string]bool{"queued": true})
	}
	if req.Path == apiPath {
		if req.Method == "GET" {
			return jsonResponse(200, p.snapshot())
		}
		if req.Method == "PUT" {
			var s settings
			d := json.NewDecoder(bytes.NewReader(req.Body))
			d.DisallowUnknownFields()
			if len(req.Body) > 1<<20 || d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
				return jsonResponse(400, map[string]string{"error": "Invalid settings JSON"})
			}
			if err := p.save(s); err != nil {
				return jsonResponse(400, map[string]string{"error": err.Error()})
			}
			return jsonResponse(200, p.snapshot())
		}
	}
	return jsonResponse(404, map[string]string{"error": "Not found"})
}
