package usagecompat

import (
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	log "github.com/sirupsen/logrus"
)

func restoreCaptureStore() {
	path := defaultSQLiteDetailStorePath() + ".captures.sqlite3"
	if _, err := os.Stat(path); err == nil {
		if errOpen := diagnostics.Default.Open(path); errOpen != nil {
			log.Warn("diagnostic capture storage unavailable; capture remains disabled")
		}
	}
}

func captureStore(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	if err := diagnostics.Default.Open(defaultSQLiteDetailStorePath() + ".captures.sqlite3"); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Diagnostic storage unavailable"})
		return false
	}
	return true
}
func (h *Handler) GetCaptureStatus(c *gin.Context) {
	if !captureStore(c) {
		return
	}
	c.JSON(http.StatusOK, diagnostics.Default.Status())
}
func (h *Handler) SetCaptureStatus(c *gin.Context) {
	if !captureStore(c) {
		return
	}
	var input struct {
		Enabled         *bool `json:"enabled"`
		DurationSeconds *int  `json:"duration_seconds"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled must be a boolean"})
		return
	}
	duration := diagnostics.Window
	if input.DurationSeconds != nil {
		switch *input.DurationSeconds {
		case 10, 20, 30:
			duration = time.Duration(*input.DurationSeconds) * time.Second
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "duration_seconds must be 10, 20 or 30"})
			return
		}
	}
	if *input.Enabled {
		if !redisqueue.UsageStatisticsEnabled() {
			c.JSON(http.StatusConflict, gin.H{"error": "Enable usage statistics before capturing"})
			return
		}
		if err := diagnostics.Default.EnableFor(duration); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Unable to start capture"})
			return
		}
	} else {
		diagnostics.Default.Disable()
	}
	c.JSON(http.StatusOK, diagnostics.Default.Status())
}
func validCaptureID(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid capture ID"})
		return false
	}
	return true
}
func (h *Handler) GetCapture(c *gin.Context) {
	if !validCaptureID(c) || !captureStore(c) {
		return
	}
	items, err := diagnostics.Default.Get(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Unable to read capture"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
func (h *Handler) DeleteCapture(c *gin.Context) {
	if !validCaptureID(c) || !captureStore(c) {
		return
	}
	if err := diagnostics.Default.Delete(c.Param("id")); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Capture is active or storage is unavailable; retry after completion"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
