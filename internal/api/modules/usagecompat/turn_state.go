package usagecompat

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	log "github.com/sirupsen/logrus"
)

func restoreTurnStateStore() {
	path := defaultSQLiteDetailStorePath() + ".turn-state.sqlite3"
	if _, err := os.Stat(path); err == nil {
		if errOpen := turnstate.Default.Open(path); errOpen != nil {
			log.Warn("turn state storage unavailable; automatic rules remain disabled")
		}
	}
}

func turnStateStore(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if err := turnstate.Default.Open(defaultSQLiteDetailStorePath() + ".turn-state.sqlite3"); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Turn state storage unavailable"})
		return false
	}
	return true
}

func (h *Handler) GetTurnStateSettings(c *gin.Context) {
	if turnStateStore(c) {
		c.JSON(http.StatusOK, turnstate.Default.Status())
	}
}

func (h *Handler) SetTurnStateSettings(c *gin.Context) {
	if !turnStateStore(c) {
		return
	}
	var input struct {
		Enabled         *bool     `json:"enabled"`
		MaxChars        *int      `json:"max_chars"`
		LifetimeSeconds *int      `json:"lifetime_seconds"`
		AccountScope    *string   `json:"account_scope"`
		AuthIDs         *[]string `json:"auth_ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 3<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Enabled == nil || input.MaxChars == nil || *input.MaxChars < 1 || *input.MaxChars > turnstate.MaxChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled and integer max_chars (1..8192) are required"})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected one JSON object"})
		return
	}
	settings := turnstate.Settings{Enabled: *input.Enabled, MaxChars: *input.MaxChars}
	if input.LifetimeSeconds != nil {
		settings.LifetimeSeconds = *input.LifetimeSeconds
	} else {
		settings.LifetimeSeconds = turnstate.Default.Status().LifetimeSeconds
	}
	if input.AccountScope != nil {
		if *input.AccountScope != "all" && *input.AccountScope != "selected" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "account_scope must be all or selected"})
			return
		}
		settings.AccountScope = *input.AccountScope
	}
	if input.AuthIDs != nil {
		settings.AuthIDs = *input.AuthIDs
	}
	_, err := turnstate.NormalizeSettings(settings)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := turnstate.Default.PatchSettings(*input.Enabled, *input.MaxChars, input.AccountScope, input.AuthIDs, input.LifetimeSeconds); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Unable to persist turn state settings"})
		return
	}
	c.JSON(http.StatusOK, turnstate.Default.Status())
}
