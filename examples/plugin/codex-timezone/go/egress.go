package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

const geoURL = "https://ipwho.is/"

func routeKey(proxy string) string {
	if proxy == "" {
		return "host-global"
	}
	hash := sha256.Sum256([]byte(proxy))
	return hex.EncodeToString(hash[:])
}

func parseLocation(body []byte) (observation, error) {
	var data struct {
		Success  bool   `json:"success"`
		IP       string `json:"ip"`
		Timezone struct {
			ID string `json:"id"`
		} `json:"timezone"`
	}
	if len(body) > 65536 || json.Unmarshal(body, &data) != nil || !data.Success || net.ParseIP(data.IP) == nil || !validTimezone(data.Timezone.ID) {
		return observation{}, errors.New("IP service returned an invalid IP or timezone")
	}
	return observation{IP: data.IP, Timezone: data.Timezone.ID}, nil
}

func (p *timezonePlugin) lookup(ctx context.Context, proxy string) (observation, error) {
	if proxy == "" {
		var result pluginapi.HTTPResponse
		// The host owns its current global proxy; do not duplicate that secret in settings.
		if err := p.host("host.http.do", map[string]any{"method": "GET", "url": geoURL}, &result); err != nil {
			return observation{}, errors.New("global proxy IP lookup failed")
		}
		if result.StatusCode != 200 {
			return observation{}, fmt.Errorf("IP service returned HTTP %d", result.StatusCode)
		}
		return parseLocation(result.Body)
	}
	tr, _, err := proxyutil.BuildHTTPTransport(proxy)
	if err != nil || tr == nil {
		return observation{}, errors.New("invalid account proxy; direct fallback is disabled")
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geoURL, nil)
	if err != nil {
		return observation{}, errors.New("IP lookup request failed")
	}
	resp, err := client.Do(req)
	if err != nil {
		return observation{}, errors.New("account proxy IP lookup failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return observation{}, fmt.Errorf("IP service returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return observation{}, errors.New("IP service response could not be read")
	}
	return parseLocation(body)
}

func (p *timezonePlugin) refresh(ctx context.Context, force bool) {
	p.mu.Lock()
	p.refreshing = true
	gen, cfg := p.generation, p.settings
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.refreshing = false; p.mu.Unlock() }()
	var list struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if err := p.host("host.auth.list", struct{}{}, &list); err != nil {
		p.mu.Lock()
		p.status = "Account list unavailable"
		p.accounts = map[string]accountView{}
		p.mu.Unlock()
		return
	}
	accounts := make(map[string]accountView)
	routes := make(map[string]string)
	for _, entry := range list.Files {
		if ctx.Err() != nil {
			return
		}
		if entry.ID == "" || (entry.Provider != "codex" && entry.Type != "codex") {
			continue
		}
		a := accountView{ID: entry.ID, Name: entry.Name, Label: entry.Label, Index: entry.AuthIndex, Route: "unknown"}
		if a.Label == "" {
			a.Label = entry.Email
		}
		if cfg.Enabled && effectiveRule(cfg, a.ID).Mode == "auto" && !entry.Disabled {
			a.Route = "global"
			var raw pluginapi.HostAuthGetResponse
			// Read only through the public callback; credentials never leave this process.
			if err := p.host("host.auth.get", map[string]string{"auth_index": entry.AuthIndex}, &raw); err != nil {
				a.Error = "Account proxy unavailable; automatic rewrite skipped"
			} else {
				var auth struct {
					ProxyURL string `json:"proxy_url"`
				}
				if json.Unmarshal(raw.JSON, &auth) != nil {
					a.Error = "Account proxy unavailable; automatic rewrite skipped"
				} else {
					a.proxy = strings.TrimSpace(auth.ProxyURL)
					setting, err := proxyutil.Parse(a.proxy)
					if err != nil {
						a.Error = "Invalid account proxy; automatic rewrite skipped"
					}
					if setting.Mode == proxyutil.ModeProxy {
						a.Route = "account"
					}
					if setting.Mode == proxyutil.ModeDirect {
						a.Route = "direct"
						a.proxy = "direct"
					}
				}
			}
			a.key = routeKey(a.proxy)
			if a.Error == "" {
				routes[a.key] = a.proxy
			}
		}
		accounts[a.ID] = a
	}
	p.mu.Lock()
	if p.generation != gen {
		p.mu.Unlock()
		return
	}
	p.accounts, p.status = accounts, ""
	for key := range p.cache {
		if _, used := routes[key]; !used {
			delete(p.cache, key)
		}
	}
	p.mu.Unlock()
	for key, proxy := range routes {
		if ctx.Err() != nil {
			return
		}
		p.mu.RLock()
		previous := p.cache[key]
		p.mu.RUnlock()
		now := p.now()
		if !force && ((previous.Error == "" && !previous.UpdatedAt.IsZero() && now.Sub(previous.UpdatedAt) < 6*time.Hour) || (!previous.AttemptedAt.IsZero() && now.Sub(previous.AttemptedAt) < 5*time.Minute)) {
			continue
		}
		result, err := p.probe(ctx, proxy)
		if err != nil {
			result = previous
			result.Error = err.Error()
		} else {
			result.UpdatedAt = now
		}
		result.AttemptedAt = now
		p.mu.Lock()
		if p.generation == gen {
			p.cache[key] = result
		}
		p.mu.Unlock()
	}
}
