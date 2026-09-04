package usagecompat

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

var errUsageStatisticsConfigUnavailable = errors.New("usage statistics config unavailable")

// StatisticsStore provides the usage aggregation methods needed by the compatibility routes.
type StatisticsStore interface {
	Snapshot() StatisticsSnapshot
	MergeSnapshot(snapshot StatisticsSnapshot) MergeResult
}

type quotaEstimatorStore interface {
	QuotaEstimatorOverview(options QuotaEstimatorOptions) (QuotaEstimatorOverview, error)
}

// StatisticsEnabledController reads and updates the usage-statistics-enabled setting.
type StatisticsEnabledController interface {
	Enabled() (bool, error)
	SetEnabled(enabled bool) error
}

// ControllerOption customizes the file-backed config controller.
type ControllerOption func(*FileBackedConfigController)

// WithConfigLocker serializes config writes when the caller has a shared lock.
func WithConfigLocker(locker sync.Locker) ControllerOption {
	return func(controller *FileBackedConfigController) {
		if controller != nil && locker != nil {
			controller.mu = locker
		}
	}
}

// WithRuntimeToggle overrides the runtime callback that applies the new enabled state immediately.
func WithRuntimeToggle(toggle func(bool)) ControllerOption {
	return func(controller *FileBackedConfigController) {
		if controller != nil && toggle != nil {
			controller.applyRuntime = toggle
		}
	}
}

// FileBackedConfigController updates usage-statistics-enabled in memory and persists it to config.yaml.
type FileBackedConfigController struct {
	mu           sync.Locker
	cfg          *config.Config
	configPath   string
	applyRuntime func(bool)
}

// NewFileBackedConfigController creates a controller backed by config.yaml persistence.
func NewFileBackedConfigController(cfg *config.Config, configPath string, opts ...ControllerOption) *FileBackedConfigController {
	controller := &FileBackedConfigController{
		mu:           &sync.Mutex{},
		cfg:          cfg,
		configPath:   strings.TrimSpace(configPath),
		applyRuntime: redisqueue.SetUsageStatisticsEnabled,
	}
	for i := range opts {
		opts[i](controller)
	}
	return controller
}

// SetConfig replaces the config pointer after a hot reload.
func (c *FileBackedConfigController) SetConfig(cfg *config.Config) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
}

// Enabled returns the current usage-statistics-enabled value.
func (c *FileBackedConfigController) Enabled() (bool, error) {
	if c == nil {
		return false, errUsageStatisticsConfigUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg == nil {
		return false, errUsageStatisticsConfigUnavailable
	}
	return c.cfg.UsageStatisticsEnabled, nil
}

// SetEnabled updates the in-memory config, applies the runtime toggle, and persists when a file is configured.
func (c *FileBackedConfigController) SetEnabled(enabled bool) error {
	if c == nil {
		return errUsageStatisticsConfigUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg == nil {
		return errUsageStatisticsConfigUnavailable
	}

	c.cfg.UsageStatisticsEnabled = enabled
	if c.applyRuntime != nil {
		c.applyRuntime(enabled)
	}
	if c.configPath == "" {
		return nil
	}
	return config.SaveConfigPreserveComments(c.configPath, c.cfg)
}

type usageExportPayload struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                `json:"version"`
	Usage   StatisticsSnapshot `json:"usage"`
}

const defaultUsageSnapshotDetailLimit = 256

// Handler serves the compatibility usage endpoints.
type Handler struct {
	stats      StatisticsStore
	controller StatisticsEnabledController
}

// NewHandler constructs a compatibility handler for the usage endpoints.
func NewHandler(stats StatisticsStore, controller StatisticsEnabledController) *Handler {
	return &Handler{
		stats:      stats,
		controller: controller,
	}
}

// GetUsageStatistics returns a lightweight request statistics snapshot for the dashboard.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	snapshot := h.dashboardSnapshot(c)
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// GetUsageDetails returns persisted request details in pages.
func (h *Handler) GetUsageDetails(c *gin.Context) {
	if h == nil || h.stats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage statistics unavailable"})
		return
	}
	store, ok := h.stats.(interface {
		DetailsPage(DetailPageQuery) DetailPage
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage details unavailable"})
		return
	}

	query := DetailPageQuery{
		API:       strings.TrimSpace(c.Query("api")),
		Model:     strings.TrimSpace(c.Query("model")),
		Source:    strings.TrimSpace(c.Query("source")),
		AuthIndex: strings.TrimSpace(c.Query("auth_index")),
		Search:    firstNonEmptyQuery(c, "search", "q", "keyword"),
		Page:      parsePositiveInt(c.Query("page"), 1),
		PageSize:  parsePositiveInt(c.Query("page_size"), defaultDetailPageSize),
		Offset:    parseNonNegativeInt(c.Query("offset"), -1),
	}
	c.JSON(http.StatusOK, store.DetailsPage(query))
}

// GetQuotaEstimatorOverview returns Codex quota capacity estimates using built-in prices.
func (h *Handler) GetQuotaEstimatorOverview(c *gin.Context) {
	h.quotaEstimatorOverview(c, QuotaEstimatorOptions{})
}

// PostQuotaEstimatorOverview returns Codex quota capacity estimates using caller-supplied prices.
func (h *Handler) PostQuotaEstimatorOverview(c *gin.Context) {
	var options QuotaEstimatorOptions
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&options); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid quota estimator options"})
			return
		}
	}
	h.quotaEstimatorOverview(c, options)
}

func (h *Handler) quotaEstimatorOverview(c *gin.Context, options QuotaEstimatorOptions) {
	store, ok := h.stats.(quotaEstimatorStore)
	if !ok || store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota estimator unavailable"})
		return
	}
	overview, err := store.QuotaEstimatorOverview(options)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, overview)
}

// GetUsageAggregate returns compact SQLite-backed aggregates for the usage dashboard.
func (h *Handler) GetUsageAggregate(c *gin.Context) {
	if h == nil || h.stats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage statistics unavailable"})
		return
	}
	store, ok := h.stats.(interface {
		Aggregate(AggregateQuery) AggregateSnapshot
	})
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "usage aggregate unavailable"})
		return
	}
	c.JSON(http.StatusOK, store.Aggregate(parseAggregateQuery(c)))
}

// ExportUsageStatistics returns a complete usage snapshot for backup or migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      h.snapshot(),
	})
}

// ImportUsageStatistics merges an exported usage snapshot into the active store.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || h.stats == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result := h.stats.MergeSnapshot(payload.Usage)
	snapshot := h.summarySnapshot()
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}

// GetUsageStatisticsEnabled returns the compatibility view of usage-statistics-enabled.
func (h *Handler) GetUsageStatisticsEnabled(c *gin.Context) {
	if h == nil || h.controller == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errUsageStatisticsConfigUnavailable.Error()})
		return
	}

	enabled, err := h.controller.Enabled()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"usage-statistics-enabled": enabled})
}

// PutUsageStatisticsEnabled updates usage-statistics-enabled using the legacy request body shape.
func (h *Handler) PutUsageStatisticsEnabled(c *gin.Context) {
	if h == nil || h.controller == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errUsageStatisticsConfigUnavailable.Error()})
		return
	}

	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	if err := h.controller.SetEnabled(*body.Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) snapshot() StatisticsSnapshot {
	if h == nil || h.stats == nil {
		return StatisticsSnapshot{}
	}
	return h.stats.Snapshot()
}

func (h *Handler) dashboardSnapshot(c *gin.Context) StatisticsSnapshot {
	if h == nil || h.stats == nil {
		return StatisticsSnapshot{}
	}

	options := SnapshotOptions{DetailLimit: defaultUsageSnapshotDetailLimit}
	if c != nil {
		options.DetailLimit = parseDetailLimit(c, options.DetailLimit)
	}
	return h.snapshotWithOptions(options)
}

func (h *Handler) summarySnapshot() StatisticsSnapshot {
	if h == nil || h.stats == nil {
		return StatisticsSnapshot{}
	}
	return h.snapshotWithOptions(SnapshotOptions{DetailLimit: 0})
}

func (h *Handler) snapshotWithOptions(options SnapshotOptions) StatisticsSnapshot {
	if store, ok := h.stats.(interface {
		SnapshotWithOptions(SnapshotOptions) StatisticsSnapshot
	}); ok {
		return store.SnapshotWithOptions(options)
	}
	return h.stats.Snapshot()
}

func parseDetailLimit(c *gin.Context, fallback int) int {
	if c == nil || c.Request == nil {
		return fallback
	}
	query := c.Request.URL.Query()
	for _, key := range []string{"detail_limit", "details_limit", "limit", "count"} {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			continue
		}
		if strings.EqualFold(raw, "all") || strings.EqualFold(raw, "full") {
			return SnapshotAllDetails
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		if value < 0 {
			return SnapshotAllDetails
		}
		return value
	}
	rawDetails := strings.TrimSpace(query.Get("details"))
	if strings.EqualFold(rawDetails, "all") || strings.EqualFold(rawDetails, "full") {
		return SnapshotAllDetails
	}
	if strings.EqualFold(rawDetails, "false") || rawDetails == "0" {
		return 0
	}
	return fallback
}

func parsePositiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func parseNonNegativeInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func firstNonEmptyQuery(c *gin.Context, keys ...string) string {
	if c == nil {
		return ""
	}
	for _, key := range keys {
		value := strings.TrimSpace(c.Query(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func parseAggregateQuery(c *gin.Context) AggregateQuery {
	now := time.Now()
	query := AggregateQuery{
		Range: strings.TrimSpace(c.Query("range")),
		Until: now,
	}
	switch strings.ToLower(query.Range) {
	case "7h":
		query.Since = now.Add(-7 * time.Hour)
	case "24h":
		query.Since = now.Add(-24 * time.Hour)
	case "7d":
		query.Since = now.AddDate(0, 0, -7)
	case "today":
		query.Since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case "all", "":
		query.Range = "all"
		query.Since = time.Time{}
	default:
		query.Range = "all"
		query.Since = time.Time{}
	}
	return query
}
