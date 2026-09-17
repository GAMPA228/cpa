package usagecompat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const defaultPathPrefix = "/v0/management"

// Option customizes the usage compatibility module.
type Option func(*Module)

// WithPathPrefix overrides the route group prefix used by the module.
func WithPathPrefix(prefix string) Option {
	return func(module *Module) {
		if module != nil {
			module.pathPrefix = strings.TrimSpace(prefix)
		}
	}
}

// WithMiddleware appends middleware applied only to the compatibility routes.
func WithMiddleware(middleware ...gin.HandlerFunc) Option {
	return func(module *Module) {
		if module != nil {
			module.middleware = append(module.middleware, middleware...)
		}
	}
}

// Module encapsulates usage-related management routes so they can be mounted independently.
type Module struct {
	pathPrefix string
	middleware []gin.HandlerFunc
	handler    *Handler
}

// New creates a usage compatibility module backed by the supplied handler.
func New(handler *Handler, opts ...Option) *Module {
	module := &Module{
		pathPrefix: defaultPathPrefix,
		handler:    handler,
	}
	for i := range opts {
		opts[i](module)
	}
	return module
}

// Name returns the module identifier used for diagnostics.
func (m *Module) Name() string { return "usagecompat" }

// Register mounts the compatibility routes on the supplied Gin engine.
func (m *Module) Register(engine *gin.Engine) error {
	if engine == nil {
		return fmt.Errorf("usagecompat: engine is nil")
	}
	if m == nil || m.handler == nil {
		return fmt.Errorf("usagecompat: handler is nil")
	}

	prefix := strings.TrimSpace(m.pathPrefix)
	if prefix == "" {
		prefix = defaultPathPrefix
	}
	group := engine.Group(prefix)
	if len(m.middleware) > 0 {
		group.Use(m.middleware...)
	}
	m.RegisterRoutes(group)
	return nil
}

// OnConfigUpdated refreshes the config reference when the server hot-reloads.
func (m *Module) OnConfigUpdated(cfg *config.Config) error {
	if m == nil || m.handler == nil {
		return nil
	}
	var updateErrs []error
	if controller, ok := m.handler.controller.(interface{ SetConfig(*config.Config) }); ok {
		controller.SetConfig(cfg)
	}
	if stats, ok := m.handler.stats.(interface{ SetTrustedProxies([]string) error }); ok {
		var trustedProxies []string
		if cfg != nil {
			trustedProxies = cfg.UsageClientIP.TrustedProxies
		}
		if err := stats.SetTrustedProxies(trustedProxies); err != nil {
			updateErrs = append(updateErrs, err)
		}
	}
	return errors.Join(updateErrs...)
}

// RegisterRoutes attaches the compatibility routes to an existing route group.
func (m *Module) RegisterRoutes(group gin.IRoutes) {
	restoreCaptureStore()
	group.GET("/usage", m.handler.GetUsageStatistics)
	group.GET("/usage/aggregate", m.handler.GetUsageAggregate)
	group.GET("/usage/details", m.handler.GetUsageDetails)
	group.GET("/usage/capture", m.handler.GetCaptureStatus)
	group.PUT("/usage/capture", m.handler.SetCaptureStatus)
	group.GET("/usage/captures/:id", m.handler.GetCapture)
	group.DELETE("/usage/captures/:id", m.handler.DeleteCapture)
	group.GET("/usage/quota-estimator", m.handler.GetQuotaEstimatorOverview)
	group.POST("/usage/quota-estimator", m.handler.PostQuotaEstimatorOverview)
	group.GET("/usage/export", m.handler.ExportUsageStatistics)
	group.POST("/usage/import", m.handler.ImportUsageStatistics)
	group.GET("/usage-statistics-enabled", m.handler.GetUsageStatisticsEnabled)
	group.PUT("/usage-statistics-enabled", m.handler.PutUsageStatisticsEnabled)
	group.PATCH("/usage-statistics-enabled", m.handler.PutUsageStatisticsEnabled)
}
