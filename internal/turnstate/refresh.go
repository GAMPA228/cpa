package turnstate

import "time"

const RefreshRetryInterval = 30 * time.Second

// Refresh reserves one ordinary request to obtain new state without sending the old header.
// It lives until that request finishes, including WebSocket metadata processing.
type Refresh struct {
	manager *Manager
	key     key
	id      uint64
	active  bool
	retryAt time.Time
}

// ID distinguishes refresh handshakes from previously reused headerless connections.
func (r *Refresh) ID() uint64 { return r.id }

// Finish releases the reservation. A replay-required request has not contacted the
// upstream, so it must not delay the client's next full replay with a retry cooldown.
func (r *Refresh) Finish(attempted bool) {
	if r == nil {
		return
	}
	m := r.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshes[r.key] != r || !r.active {
		return
	}
	r.active = false
	if attempted {
		r.retryAt = m.now().Add(RefreshRetryInterval)
	}
}

// PrepareRequest resolves automatic state and optionally reserves a refresh.
// omit also removes client-supplied values, which would otherwise suppress issuance.
// Unknown account/model pairs retain their original headers until first observed.
func (m *Manager) PrepareRequest(authID, model string) (value string, omit bool, refresh *Refresh) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil || m.closing || !m.accountEnabledLocked(authID) {
		return "", false, nil
	}
	k := key{authID, model}
	rule, exists := m.rules[k]
	if !exists {
		return "", false, nil
	}
	now := m.now()
	usable := len(rule.Value) <= m.settings.MaxChars && now.Before(rule.ExpiresAt)
	if usable && now.Before(rule.ExpiresAt.Add(-m.refreshBeforeLocked())) {
		return rule.Value, false, nil
	}
	previous := m.refreshes[k]
	if previous != nil && (previous.active || now.Before(previous.retryAt)) {
		if usable {
			return rule.Value, false, nil
		}
		return "", true, nil
	}
	m.refreshSeq++
	refresh = &Refresh{manager: m, key: k, id: m.refreshSeq, active: true}
	m.refreshes[k] = refresh
	return "", true, refresh
}
