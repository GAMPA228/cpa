package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type apiKeyQuotaUpdate struct {
	APIKey          string `json:"api-key"`
	DailyTokenLimit int64  `json:"daily-token-limit"`
}

// GetAPIKeyQuotas returns daily quota status for configured downstream API keys.
func (h *Handler) GetAPIKeyQuotas(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	manager := h.apiKeyQuotaManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "api key quota manager unavailable"})
		return
	}

	statuses, err := manager.Statuses(time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": statuses})
}

// PutAPIKeyQuotas updates daily token limits for configured downstream API keys.
func (h *Handler) PutAPIKeyQuotas(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	updates, ok := parseAPIKeyQuotaUpdates(c)
	if !ok {
		return
	}
	updateByKey := make(map[string]int64, len(updates))
	for _, update := range updates {
		apiKey := strings.TrimSpace(update.APIKey)
		if apiKey == "" {
			continue
		}
		limit := update.DailyTokenLimit
		if limit < 0 {
			limit = 0
		}
		updateByKey[apiKey] = limit
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}

	h.cfg.SanitizeAPIKeyEntries()
	entries := h.cfg.APIKeyEntriesSnapshot()
	nextEntries := make(config.APIKeyEntryList, 0, len(entries))
	for _, entry := range entries {
		if limit, exists := updateByKey[entry.APIKey]; exists {
			entry.DailyTokenLimit = limit
		}
		nextEntries = append(nextEntries, entry)
	}
	h.cfg.APIKeyEntries = nextEntries
	h.cfg.SanitizeAPIKeyEntries()
	if h.apiKeyQuotaManager != nil {
		h.apiKeyQuotaManager.SetConfig(h.cfg)
	}
	h.persistLocked(c)
}

func parseAPIKeyQuotaUpdates(c *gin.Context) ([]apiKeyQuotaUpdate, bool) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return nil, false
	}

	var items []apiKeyQuotaUpdate
	if err := json.Unmarshal(data, &items); err == nil {
		return items, true
	}

	var body struct {
		Items []apiKeyQuotaUpdate `json:"items"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return nil, false
	}
	return body.Items, true
}
