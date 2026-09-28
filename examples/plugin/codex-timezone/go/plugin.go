package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginID = "codex-timezone"
const version = "0.1.2"

type rule struct {
	Mode     string `json:"mode"`
	Timezone string `json:"timezone"`
}

type settings struct {
	Enabled  bool            `json:"enabled"`
	Default  rule            `json:"default"`
	Accounts map[string]rule `json:"accounts"`
}

type accountView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"`
	Index string `json:"-"`
	Route string `json:"route"`
	Error string `json:"error,omitempty"`
	proxy string
	key   string
}

type observation struct {
	IP          string    `json:"ip,omitempty"`
	Timezone    string    `json:"timezone,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	Error       string    `json:"error,omitempty"`
	AttemptedAt time.Time `json:"attempted_at,omitempty"`
}

type hostCall func(string, any, any) error

type timezonePlugin struct {
	mu         sync.RWMutex
	settings   settings
	path       string
	accounts   map[string]accountView
	cache      map[string]observation
	status     string
	refreshing bool
	generation uint64
	host       hostCall
	probe      func(context.Context, string) (observation, error)
	now        func() time.Time
	wake       chan bool
	cancel     context.CancelFunc
	done       chan struct{}
	lastTouch  atomic.Int64
}

func newPlugin(host hostCall) *timezonePlugin {
	p := &timezonePlugin{settings: settings{Default: rule{Mode: "keep"}, Accounts: map[string]rule{}}, accounts: map[string]accountView{}, cache: map[string]observation{}, host: host, now: time.Now, wake: make(chan bool, 1)}
	p.probe = p.lookup
	return p
}

var active = newPlugin(func(string, any, any) error { return errors.New("host unavailable") })

func main() {}

func validateSettings(s settings) error {
	check := func(r rule, inherit bool) bool {
		switch r.Mode {
		case "keep", "auto":
			return true
		case "manual":
			return validTimezone(r.Timezone)
		case "inherit":
			return inherit
		default:
			return false
		}
	}
	if !check(s.Default, false) || len(s.Accounts) > 10000 {
		return errors.New("invalid default mode or timezone")
	}
	for id, r := range s.Accounts {
		if strings.TrimSpace(id) == "" || !check(r, true) {
			return errors.New("invalid account rule")
		}
	}
	return nil
}

func (p *timezonePlugin) configure(raw []byte) error {
	var request struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	cfg := struct {
		DataFile string `yaml:"data_file"`
	}{DataFile: "plugins/data/codex-timezone.json"}
	if err := yaml.Unmarshal(request.ConfigYAML, &cfg); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.DataFile) == "" {
		return errors.New("data_file is required")
	}
	s := settings{Default: rule{Mode: "keep"}, Accounts: map[string]rule{}}
	data, err := os.ReadFile(cfg.DataFile)
	if err == nil {
		if err = json.Unmarshal(data, &s); err != nil {
			return errors.New("invalid settings file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("settings file is not readable")
	}
	if err = validateSettings(s); err != nil {
		return err
	}
	p.mu.Lock()
	p.path, p.settings = cfg.DataFile, s
	p.generation++
	// A host reconfiguration can change the inherited global proxy.
	p.cache = map[string]observation{}
	if p.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel, p.done = cancel, make(chan struct{})
		go p.run(ctx, p.done)
	}
	p.mu.Unlock()
	p.requestRefresh(false)
	return nil
}

func (p *timezonePlugin) save(s settings) error {
	if err := validateSettings(s); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" {
		return errors.New("storage unavailable")
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p.path), 0700); err != nil {
		return errors.New("settings directory is not writable")
	}
	f, err := os.CreateTemp(filepath.Dir(p.path), ".timezone-*")
	if err != nil {
		return errors.New("settings directory is not writable")
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	errClose := f.Close()
	if err != nil || errClose != nil {
		return errors.New("settings write failed")
	}
	if err = os.Rename(name, p.path); err != nil {
		return errors.New("settings replace failed")
	}
	p.settings = s
	p.generation++
	p.requestRefresh(false)
	return nil
}

func (p *timezonePlugin) requestRefresh(force bool) {
	select {
	case p.wake <- force:
	default:
		if force {
			select {
			case <-p.wake:
			default:
			}
			select {
			case p.wake <- true:
			default:
			}
		}
	}
}

func (p *timezonePlugin) close() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (p *timezonePlugin) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case force := <-p.wake:
			p.refresh(ctx, force)
		case <-ticker.C:
			if p.now().Sub(time.Unix(0, p.lastTouch.Load())) < 2*time.Minute {
				p.refresh(ctx, false)
			}
		}
	}
}

// The host may detach hooks without quiescing a generic plugin. Stop idle polling
// when neither requests nor the plugin panel are using this instance.
func (p *timezonePlugin) touch() {
	now := p.now()
	previous := p.lastTouch.Swap(now.UnixNano())
	if now.Sub(time.Unix(0, previous)) > time.Minute {
		p.requestRefresh(false)
	}
}

func effectiveRule(s settings, id string) rule {
	if r, ok := s.Accounts[id]; ok && r.Mode != "inherit" {
		return r
	}
	return s.Default
}

func (p *timezonePlugin) intercept(req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if !strings.EqualFold(req.ToFormat, "codex") && req.ToFormat != "openai-response" {
		return pluginapi.RequestInterceptResponse{}
	}
	id, _ := req.Metadata["selected_auth_id"].(string)
	if id == "" {
		return pluginapi.RequestInterceptResponse{}
	}
	p.mu.RLock()
	// Other providers can also target the Codex protocol. Require a known Codex account.
	_, knownAccount := p.accounts[id]
	if !p.settings.Enabled || !knownAccount {
		p.mu.RUnlock()
		return pluginapi.RequestInterceptResponse{}
	}
	r := effectiveRule(p.settings, id)
	zone := ""
	if r.Mode == "manual" {
		zone = r.Timezone
	}
	if r.Mode == "auto" {
		if a, ok := p.accounts[id]; ok && a.Error == "" {
			zone = p.cache[a.key].Timezone
		}
	}
	p.mu.RUnlock()
	if zone == "" {
		return pluginapi.RequestInterceptResponse{}
	}
	body := rewriteBody(req.Body, zone)
	if bytes.Equal(body, req.Body) {
		return pluginapi.RequestInterceptResponse{}
	}
	return pluginapi.RequestInterceptResponse{Body: body}
}
