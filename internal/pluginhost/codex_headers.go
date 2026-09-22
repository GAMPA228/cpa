package pluginhost

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (h *Host) codexHeaderPlugin() (string, pluginapi.CodexHeaderPlugin) {
	if h == nil {
		return "", nil
	}
	var id string
	var selected pluginapi.CodexHeaderPlugin
	for _, record := range h.activeRecords() {
		if capability := record.plugin.Capabilities.CodexHeaderPlugin; capability != nil && !h.isPluginFused(record.id) {
			if selected != nil {
				return "", nil
			}
			id, selected = record.id, capability
		}
	}
	return id, selected
}

// HasCodexHeaderPlugin refuses ambiguous configurations rather than selecting
// a plugin that may have a different account state or cookie jar.
func (h *Host) HasCodexHeaderPlugin() bool {
	_, capability := h.codexHeaderPlugin()
	return capability != nil
}

// Disabling a stateful header plugin must release cookies and refresh reservations.
// Other capabilities retain the host's existing lifecycle behavior.
func (h *Host) quiesceRemovedHeaderPlugins(ctx context.Context, previous []capabilityRecord) {
	active := make(map[string]bool)
	for _, record := range h.activeRecords() {
		active[record.id] = true
	}
	for _, record := range previous {
		if record.plugin.Capabilities.CodexHeaderPlugin == nil || active[record.id] {
			continue
		}
		h.mu.Lock()
		loaded := h.loaded[record.id]
		h.mu.Unlock()
		if loaded != nil {
			h.callQuiesce(context.WithoutCancel(ctx), loaded)
		}
	}
}

func (h *Host) PrepareCodexHeaders(ctx context.Context, req pluginapi.CodexHeaderRequest) (resp pluginapi.CodexHeaderResponse, err error) {
	id, capability := h.codexHeaderPlugin()
	if capability == nil {
		return resp, fmt.Errorf("codex header plugin unavailable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(id, "CodexHeaderPlugin.PrepareCodexHeaders", recovered)
			err = fmt.Errorf("codex header plugin failed")
		}
	}()
	return capability.PrepareCodexHeaders(ctx, req)
}

func (h *Host) ObserveCodexHeaders(ctx context.Context, req pluginapi.CodexHeaderObservation) (err error) {
	id, capability := h.codexHeaderPlugin()
	if capability == nil {
		return fmt.Errorf("codex header plugin unavailable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(id, "CodexHeaderPlugin.ObserveCodexHeaders", recovered)
			err = fmt.Errorf("codex header plugin failed")
		}
	}()
	return capability.ObserveCodexHeaders(ctx, req)
}

func (h *Host) CompleteCodexHeaders(ctx context.Context, req pluginapi.CodexHeaderCompletion) (err error) {
	id, capability := h.codexHeaderPlugin()
	if capability == nil {
		return fmt.Errorf("codex header plugin unavailable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(id, "CodexHeaderPlugin.CompleteCodexHeaders", recovered)
			err = fmt.Errorf("codex header plugin failed")
		}
	}()
	return capability.CompleteCodexHeaders(ctx, req)
}
