package usagecompat

import (
	"encoding/json"
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
		Enabled  *bool `json:"enabled"`
		MaxChars *int  `json:"max_chars"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Enabled == nil || input.MaxChars == nil || *input.MaxChars < 1 || *input.MaxChars > turnstate.MaxChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled and integer max_chars (1..8192) are required"})
		return
	}
	if err := turnstate.Default.Configure(turnstate.Settings{Enabled: *input.Enabled, MaxChars: *input.MaxChars}); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Unable to persist turn state settings"})
		return
	}
	c.JSON(http.StatusOK, turnstate.Default.Status())
}
