package usagecompat

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const turnStatePluginEndpoint = "/v0/management/plugins/codex-headers/turn-state"

// Keep legacy routes authenticated without opening the plugin-owned store.
func (h *Handler) GetTurnStateSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusGone, gin.H{
		"error":       "Turn State settings are managed by the codex-headers plugin",
		"replacement": turnStatePluginEndpoint,
	})
}

func (h *Handler) SetTurnStateSettings(c *gin.Context) {
	h.GetTurnStateSettings(c)
}
