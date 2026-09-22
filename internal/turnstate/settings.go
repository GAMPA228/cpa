package turnstate

import (
	"errors"
	"sort"
	"strings"
)

// NormalizeSettings returns an owned, validated snapshot, including legacy defaults.
func NormalizeSettings(settings Settings) (Settings, error) {
	if settings.LifetimeSeconds == 0 {
		settings.LifetimeSeconds = DefaultLifetimeSeconds
	}
	if settings.LifetimeSeconds < 60 || settings.LifetimeSeconds > 3600 {
		return Settings{}, errors.New("lifetime_seconds must be between 60 and 3600")
	}
	if settings.MaxChars < 1 || settings.MaxChars > MaxChars {
		return Settings{}, errors.New("max_chars must be an integer between 1 and 8192")
	}
	if settings.AccountScope == "" {
		settings.AccountScope = "all"
	}
	if settings.AccountScope != "all" && settings.AccountScope != "selected" {
		return Settings{}, errors.New("account_scope must be all or selected")
	}
	if len(settings.AuthIDs) > 4096 {
		return Settings{}, errors.New("auth_ids supports at most 4096 accounts")
	}
	ids := make(map[string]struct{}, len(settings.AuthIDs))
	for _, id := range settings.AuthIDs {
		if strings.TrimSpace(id) != id || id == "" || len(id) > 512 || strings.ContainsAny(id, "\r\n\x00") {
			return Settings{}, errors.New("invalid auth_id")
		}
		ids[id] = struct{}{}
	}
	settings.AuthIDs = make([]string, 0, len(ids))
	for id := range ids {
		settings.AuthIDs = append(settings.AuthIDs, id)
	}
	sort.Strings(settings.AuthIDs)
	return settings, nil
}

// setSettingsLocked stores only validated, privately owned settings.
func (m *Manager) setSettingsLocked(settings Settings) {
	m.settings = settings
	m.accounts = make(map[string]struct{}, len(settings.AuthIDs))
	for _, id := range settings.AuthIDs {
		m.accounts[id] = struct{}{}
	}
}

func (m *Manager) accountEnabledLocked(authID string) bool {
	if !m.settings.Enabled {
		return false
	}
	if m.settings.AccountScope == "all" {
		return true
	}
	_, ok := m.accounts[authID]
	return ok
}

func (m *Manager) AccountEnabled(authID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accountEnabledLocked(authID)
}
