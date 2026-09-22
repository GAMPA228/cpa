package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"golang.org/x/net/publicsuffix"
	"gopkg.in/yaml.v3"
)

type headerPlugin struct {
	mu           sync.RWMutex
	manager      *turnstate.Manager
	path         string
	jars         map[string]http.CookieJar
	reservations map[string]*turnstate.Refresh
	now          func() time.Time
}

type pluginConfig struct {
	DataPath string `yaml:"data_path"`
}
type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type pluginRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  struct {
		CodexHeaderPlugin bool `json:"codex_header_plugin"`
		ManagementAPI     bool `json:"management_api"`
	} `json:"capabilities"`
}

func registration() pluginRegistration {
	result := pluginRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name: "codex-headers", Version: "0.1.0", Author: "CLIProxyAPI",
			GitHubRepository: "https://github.com/GAMPA228/cpa",
			ConfigFields: []pluginapi.ConfigField{{
				Name: "data_path", Type: pluginapi.ConfigFieldTypeString,
				Description: "Optional absolute SQLite path for Turn State settings and rules. Defaults to the existing usagecompat Turn State database.",
			}},
		},
	}
	result.Capabilities.CodexHeaderPlugin = true
	result.Capabilities.ManagementAPI = true
	return result
}

func newHeaderPlugin() *headerPlugin {
	return &headerPlugin{jars: make(map[string]http.CookieJar), reservations: make(map[string]*turnstate.Refresh), now: time.Now}
}

func (p *headerPlugin) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.reservations {
		r.Finish(false)
	}
	p.reservations = make(map[string]*turnstate.Refresh)
	if p.manager != nil {
		p.manager.Close()
		p.manager = nil
	}
	p.jars = make(map[string]http.CookieJar)
	p.path = ""
}

func (p *headerPlugin) configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 && json.Unmarshal(raw, &req) != nil {
		return errors.New("invalid lifecycle payload")
	}
	var cfg pluginConfig
	if len(req.ConfigYAML) > 0 && yaml.Unmarshal(req.ConfigYAML, &cfg) != nil {
		return errors.New("invalid plugin configuration")
	}
	if cfg.DataPath != "" && !filepath.IsAbs(cfg.DataPath) {
		return errors.New("data_path must be an absolute path")
	}
	if cfg.DataPath == "" {
		base := strings.TrimSpace(os.Getenv("USAGECOMPAT_SQLITE_PATH"))
		if base == "" {
			base = "usagecompat.sqlite3"
		}
		path, err := filepath.Abs(base + ".turn-state.sqlite3")
		if err != nil {
			return errors.New("cannot resolve turn state storage")
		}
		cfg.DataPath = path
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg.DataPath == p.path {
		return nil
	}
	var next *turnstate.Manager
	if cfg.DataPath != "" {
		next = turnstate.NewManager()
		if err := next.Open(cfg.DataPath); err != nil {
			return errors.New("cannot open turn state storage")
		}
	}
	for _, r := range p.reservations {
		r.Finish(false)
	}
	p.reservations = make(map[string]*turnstate.Refresh)
	if p.manager != nil {
		p.manager.Close()
	}
	p.manager, p.path = next, cfg.DataPath
	p.jars = make(map[string]http.CookieJar)
	return nil
}

func (p *headerPlugin) prepare(raw []byte) (pluginapi.CodexHeaderResponse, error) {
	var req pluginapi.CodexHeaderRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return pluginapi.CodexHeaderResponse{}, errors.New("invalid prepare request")
	}
	var data any
	if len(req.Rules) > 0 && string(req.Rules) != "null" {
		if json.Unmarshal(req.Rules, &data) != nil {
			data = nil
		}
	}
	rules, err := authheaders.Decode(data)
	if err != nil {
		// The native implementation ignores invalid stored rules.
		rules = nil
	}
	selected := authheaders.Select(rules, req.Model, p.now())
	before := req.Headers.Clone()
	if before == nil {
		before = make(http.Header)
	}
	after := before.Clone()
	p.mu.Lock()
	defer p.mu.Unlock()
	var refresh *turnstate.Refresh
	if req.OAuth && p.manager != nil && req.AuthID != "" && p.manager.AccountEnabled(req.AuthID) {
		value, omit, reservation := p.manager.PrepareRequest(req.AuthID, req.Model)
		refresh = reservation
		if omit || value != "" {
			removeHeader(after, turnstate.Header)
			if !omit {
				after.Set(turnstate.Header, value)
			}
			action := "override"
			if omit {
				action = "delete"
			}
			selected = append(selected, authheaders.Rule{Name: turnstate.Header, Operation: action, Value: value})
		}
	}
	if req.OAuth && req.AuthID != "" {
		if u := validURL(req.URL); u != nil {
			if jar := p.jar(req.AuthID); jar != nil {
				for _, cookie := range jar.Cookies(u) {
					// Preserve any existing cookie from the request, but never merge between accounts.
					found := false
					for _, part := range strings.Split(getHeader(after, "Cookie"), ";") {
						if strings.TrimSpace(strings.SplitN(part, "=", 2)[0]) == cookie.Name {
							found = true
							break
						}
					}
					if !found {
						value := cookie.Name + "=" + cookie.Value
						if prior := getHeader(after, "Cookie"); prior != "" {
							value = prior + "; " + value
						}
						removeHeader(after, "Cookie")
						after.Set("Cookie", value)
					}
				}
			}
		}
	}
	for _, rule := range selected {
		if rule.Operation == "default" && hasHeader(after, rule.Name) {
			continue
		}
		removeHeader(after, rule.Name)
		authheaders.Apply(after, []authheaders.Rule{rule})
	}
	result := diffHeaders(before, after)
	result.Signature = authheaders.Signature(selected)
	if req.OAuth && req.AuthID != "" && validURL(req.URL) != nil {
		if cookie := getHeader(after, "Cookie"); cookie != "" {
			sum := sha256.Sum256([]byte(cookie))
			result.Signature += ":cookie:" + hex.EncodeToString(sum[:])
		}
	}
	if refresh != nil {
		id, err := newReservationID()
		if err != nil {
			refresh.Finish(false)
			return pluginapi.CodexHeaderResponse{}, errors.New("reservation unavailable")
		}
		p.reservations[id] = refresh
		result.ReservationID = id
		result.Signature += ":turn-state-refresh:" + id
	}
	return result, nil
}

func (p *headerPlugin) jar(authID string) http.CookieJar {
	if existing := p.jars[authID]; existing != nil {
		return existing
	}
	created, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil
	}
	p.jars[authID] = created
	return created
}

func validURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return nil
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return nil
	}
	return u
}

func diffHeaders(before, after http.Header) pluginapi.CodexHeaderResponse {
	result := pluginapi.CodexHeaderResponse{Headers: make(http.Header)}
	for name := range before {
		if !reflect.DeepEqual(before[name], after[name]) {
			result.ClearHeaders = append(result.ClearHeaders, name)
		}
	}
	for name, values := range after {
		old, exists := before[name]
		if !exists || !equalStrings(old, values) {
			result.Headers[name] = append([]string(nil), values...)
		}
	}
	return result
}

func removeHeader(headers http.Header, name string) {
	for actual := range headers {
		if strings.EqualFold(actual, name) {
			delete(headers, actual)
		}
	}
}

func hasHeader(headers http.Header, name string) bool {
	for actual := range headers {
		if strings.EqualFold(actual, name) {
			return true
		}
	}
	return false
}

func getHeader(headers http.Header, name string) string {
	for actual, values := range headers {
		if strings.EqualFold(actual, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newReservationID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func (p *headerPlugin) observe(raw []byte) error {
	var req pluginapi.CodexHeaderObservation
	if json.Unmarshal(raw, &req) != nil {
		return errors.New("invalid observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !req.OAuth || req.AuthID == "" {
		return nil
	}
	if p.manager != nil && p.manager.AccountEnabled(req.AuthID) {
		if value, _ := turnstate.Value(req.Headers); value != "" && (req.StatusCode == http.StatusSwitchingProtocols || req.StatusCode >= 200 && req.StatusCode < 300) {
			p.manager.Observe(req.AuthID, req.Model, value)
		}
	}
	if u := validURL(req.URL); u != nil && hasHeader(req.Headers, "Set-Cookie") {
		response := http.Response{Header: http.Header{"Set-Cookie": append([]string(nil), req.Headers["Set-Cookie"]...)}}
		for name, values := range req.Headers {
			if strings.EqualFold(name, "Set-Cookie") && name != "Set-Cookie" {
				response.Header["Set-Cookie"] = append(response.Header["Set-Cookie"], values...)
			}
		}
		if jar := p.jar(req.AuthID); jar != nil {
			jar.SetCookies(u, response.Cookies())
		}
	}
	return nil
}

func (p *headerPlugin) complete(raw []byte) error {
	var req pluginapi.CodexHeaderCompletion
	if json.Unmarshal(raw, &req) != nil {
		return errors.New("invalid completion")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r := p.reservations[req.ReservationID]; r != nil {
		delete(p.reservations, req.ReservationID)
		r.Finish(req.Attempted)
	}
	return nil
}
