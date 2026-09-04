package usagecompat

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	defaultQuotaEstimatorFastMultiplier = 2.5
	quotaEstimatorHistoryWindow         = 31 * 24 * time.Hour
)

// QuotaEstimatorPrice describes the USD price for one million tokens.
type QuotaEstimatorPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
	Cache      float64 `json:"cache"`
}

// QuotaEstimatorOptions controls the valuation used for a dashboard snapshot.
type QuotaEstimatorOptions struct {
	Prices         map[string]QuotaEstimatorPrice `json:"prices"`
	FastMultiplier float64                        `json:"fast_multiplier"`
	ApplyFast      *bool                          `json:"apply_fast"`
}

// QuotaEstimatorOverview is the native Codex quota capacity dashboard payload.
type QuotaEstimatorOverview struct {
	GeneratedAt        time.Time                       `json:"generated_at"`
	ValueUnit          string                          `json:"value_unit"`
	Accounts           []QuotaEstimatorAccountOverview `json:"accounts"`
	MissingPriceModels []string                        `json:"missing_price_models,omitempty"`
}

// QuotaEstimatorAccountOverview contains the latest observed windows for one credential.
type QuotaEstimatorAccountOverview struct {
	Account     string                        `json:"account"`
	AuthID      string                        `json:"auth_id,omitempty"`
	AuthIndex   string                        `json:"auth_index,omitempty"`
	PlanType    string                        `json:"plan_type,omitempty"`
	LatestModel string                        `json:"latest_model,omitempty"`
	Primary     QuotaEstimatorWindowOverview  `json:"primary"`
	Secondary   *QuotaEstimatorWindowOverview `json:"secondary,omitempty"`
}

// QuotaEstimatorWindowOverview describes one primary or weekly quota window.
type QuotaEstimatorWindowOverview struct {
	Scope               string    `json:"scope"`
	UsedPercent         float64   `json:"used_percent"`
	RemainingPercent    float64   `json:"remaining_percent"`
	ResetAt             time.Time `json:"reset_at"`
	WindowMinutes       int64     `json:"window_minutes"`
	LastObservedAt      time.Time `json:"last_observed_at"`
	CurrentCycleTokens  int64     `json:"current_cycle_tokens"`
	CurrentCycleCostUSD float64   `json:"current_cycle_cost_usd"`
	EstimateAvailable   bool      `json:"estimate_available"`
	FullWindowTokens    float64   `json:"full_window_tokens"`
	FullWindowCostUSD   float64   `json:"full_window_cost_usd"`
	RemainingTokens     float64   `json:"remaining_tokens"`
	RemainingCostUSD    float64   `json:"remaining_cost_usd"`
	CostLow             float64   `json:"cost_low"`
	CostHigh            float64   `json:"cost_high"`
	SampleCount         int       `json:"sample_count"`
	PercentSpan         float64   `json:"percent_span"`
	Confidence          string    `json:"confidence"`
	Status              string    `json:"status"`
}

type quotaEstimatorEvent struct {
	ID                     int64
	RequestedAt            time.Time
	ObservedAt             time.Time
	Account                string
	AuthID                 string
	AuthIndex              string
	Model                  string
	ServiceTier            string
	InputTokens            int64
	OutputTokens           int64
	CachedTokens           int64
	CacheCreationTokens    int64
	TotalTokens            int64
	Failed                 bool
	PrimaryUsedPercent     *float64
	PrimaryResetAt         int64
	PrimaryWindowMinutes   int64
	SecondaryUsedPercent   *float64
	SecondaryResetAt       int64
	SecondaryWindowMinutes int64
	PlanType               string
}

type quotaEstimatorPoint struct {
	ObservedAt time.Time
	Used       float64
	Tokens     int64
	Cost       float64
}

var defaultQuotaEstimatorPrices = map[string]QuotaEstimatorPrice{
	"gpt-5.6":       {Prompt: 4, Completion: 20, Cache: 0.4},
	"gpt-5.6-sol":   {Prompt: 4, Completion: 20, Cache: 0.4},
	"gpt-5.6-terra": {Prompt: 2, Completion: 12, Cache: 0.2},
	"gpt-5.6-luna":  {Prompt: 0.2, Completion: 1.2, Cache: 0.02},
}

func initQuotaEstimatorSchema(db *sql.DB) error {
	if db == nil {
		return nil
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS codex_quota_estimator_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			requested_at_ns INTEGER NOT NULL,
			observed_at_ns INTEGER NOT NULL,
			account TEXT NOT NULL,
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			service_tier TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			primary_used_percent REAL,
			primary_reset_at INTEGER NOT NULL DEFAULT 0,
			primary_window_minutes INTEGER NOT NULL DEFAULT 0,
			secondary_used_percent REAL,
			secondary_reset_at INTEGER NOT NULL DEFAULT 0,
			secondary_window_minutes INTEGER NOT NULL DEFAULT 0,
			plan_type TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS codex_quota_estimator_account_time_idx
			ON codex_quota_estimator_events(account, observed_at_ns DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS codex_quota_estimator_sample_time_idx
			ON codex_quota_estimator_events(primary_used_percent, observed_at_ns DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("quota estimator schema: %w", err)
		}
	}
	return nil
}

func (s *sqliteDetailStore) insertQuotaEstimatorEvent(record coreusage.Record) error {
	if s == nil || s.db == nil || !isCodexQuotaEstimatorRecord(record) {
		return nil
	}
	authID := strings.TrimSpace(record.AuthID)
	authIndex := strings.TrimSpace(record.AuthIndex)
	account := authID
	if account == "" {
		account = authIndex
	}
	if account == "" {
		return nil
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	observedAt := requestedAt
	delay := record.TTFT
	if delay <= 0 {
		delay = record.Latency
	}
	if delay > 0 && delay <= 24*time.Hour {
		observedAt = requestedAt.Add(delay)
	}
	primaryUsed, hasPrimaryUsed := quotaHeaderPercent(record.ResponseHeaders, "X-Codex-Primary-Used-Percent")
	primaryWindow, _ := quotaHeaderInt(record.ResponseHeaders, "X-Codex-Primary-Window-Minutes")
	secondaryUsed, hasSecondaryUsed := quotaHeaderPercent(record.ResponseHeaders, "X-Codex-Secondary-Used-Percent")
	secondaryWindow, _ := quotaHeaderInt(record.ResponseHeaders, "X-Codex-Secondary-Window-Minutes")
	primaryReset := quotaResetAt(record.ResponseHeaders, "X-Codex-Primary-", observedAt)
	secondaryReset := quotaResetAt(record.ResponseHeaders, "X-Codex-Secondary-", observedAt)
	var primaryValue any
	if hasPrimaryUsed {
		primaryValue = primaryUsed
	}
	var secondaryValue any
	if hasSecondaryUsed && isFiveHourQuotaWindow(primaryWindow) && isWeeklyQuotaWindow(secondaryWindow) {
		secondaryValue = secondaryUsed
	} else {
		secondaryReset = 0
		secondaryWindow = 0
	}
	totalTokens := record.Detail.TotalTokens
	if totalTokens <= 0 {
		totalTokens = record.Detail.InputTokens + record.Detail.OutputTokens
	}
	failed := 0
	if record.Failed {
		failed = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO codex_quota_estimator_events (
			requested_at_ns, observed_at_ns, account, auth_id, auth_index, model, service_tier,
			input_tokens, output_tokens, cached_tokens, cache_creation_tokens, total_tokens, failed,
			primary_used_percent, primary_reset_at, primary_window_minutes,
			secondary_used_percent, secondary_reset_at, secondary_window_minutes, plan_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		requestedAt.UTC().UnixNano(), observedAt.UTC().UnixNano(), account, authID, authIndex,
		strings.TrimSpace(record.Model), normalizedQuotaServiceTier(record),
		record.Detail.InputTokens, record.Detail.OutputTokens,
		max(record.Detail.CacheReadTokens, record.Detail.CachedTokens), record.Detail.CacheCreationTokens,
		totalTokens, failed, primaryValue, canonicalQuotaResetAt(primaryReset), primaryWindow,
		secondaryValue, canonicalQuotaResetAt(secondaryReset), secondaryWindow,
		quotaHeader(record.ResponseHeaders, "X-Codex-Plan-Type"),
	)
	return err
}

func isCodexQuotaEstimatorRecord(record coreusage.Record) bool {
	if strings.EqualFold(strings.TrimSpace(record.Provider), "codex") {
		return true
	}
	if strings.Contains(strings.ToLower(record.ExecutorType), "codex") {
		return true
	}
	return quotaHeader(record.ResponseHeaders, "X-Codex-Primary-Used-Percent") != ""
}

func normalizedQuotaServiceTier(record coreusage.Record) string {
	for _, value := range []string{record.ResponseServiceTier, record.Detail.ResponseServiceTier, record.AppliedServiceTier, record.ServiceTier, record.RequestServiceTier} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func quotaHeader(headers http.Header, key string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(key))
}

func quotaHeaderFloat(headers http.Header, key string) (float64, bool) {
	value, err := strconv.ParseFloat(quotaHeader(headers, key), 64)
	return value, err == nil
}

func quotaHeaderPercent(headers http.Header, key string) (float64, bool) {
	value, ok := quotaHeaderFloat(headers, key)
	return value, ok && value >= 0 && value <= 100
}

func quotaHeaderInt(headers http.Header, key string) (int64, bool) {
	value, err := strconv.ParseInt(quotaHeader(headers, key), 10, 64)
	return value, err == nil
}

func quotaResetAt(headers http.Header, prefix string, observedAt time.Time) int64 {
	if resetAt, ok := quotaHeaderInt(headers, prefix+"Reset-At"); ok && resetAt > 0 {
		// Tolerate millisecond timestamps from compatible Codex upstreams.
		if resetAt > 10_000_000_000 {
			resetAt /= 1000
		}
		return canonicalQuotaResetAt(resetAt)
	}
	if resetAfter, ok := quotaHeaderInt(headers, prefix+"Reset-After-Seconds"); ok && resetAfter >= 0 {
		return canonicalQuotaResetAt(observedAt.Unix() + resetAfter)
	}
	return 0
}

func canonicalQuotaResetAt(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return ((value + 30) / 60) * 60
}

func isFiveHourQuotaWindow(minutes int64) bool { return minutes >= 270 && minutes <= 330 }
func isWeeklyQuotaWindow(minutes int64) bool   { return minutes >= 9000 && minutes <= 11000 }

func (s *RequestStatistics) QuotaEstimatorOverview(options QuotaEstimatorOptions) (QuotaEstimatorOverview, error) {
	overview := QuotaEstimatorOverview{GeneratedAt: time.Now().UTC(), ValueUnit: "USD", Accounts: []QuotaEstimatorAccountOverview{}}
	if s == nil || s.detailStore == nil || s.detailStore.db == nil {
		return overview, fmt.Errorf("quota estimator storage unavailable")
	}
	return s.detailStore.quotaEstimatorOverview(options)
}

func (s *sqliteDetailStore) quotaEstimatorOverview(options QuotaEstimatorOptions) (QuotaEstimatorOverview, error) {
	result := QuotaEstimatorOverview{GeneratedAt: time.Now().UTC(), ValueUnit: "USD", Accounts: []QuotaEstimatorAccountOverview{}}
	prices := normalizeQuotaEstimatorPrices(options.Prices)
	fastMultiplier := options.FastMultiplier
	if fastMultiplier <= 0 {
		fastMultiplier = defaultQuotaEstimatorFastMultiplier
	}
	applyFast := true
	if options.ApplyFast != nil {
		applyFast = *options.ApplyFast
	}
	latest, err := s.latestQuotaEstimatorSamples()
	if err != nil {
		return result, err
	}
	missing := make(map[string]struct{})
	for _, sample := range latest {
		startAt := sample.PrimaryResetAt - sample.PrimaryWindowMinutes*60
		if sample.SecondaryUsedPercent != nil && sample.SecondaryResetAt > 0 && sample.SecondaryWindowMinutes > 0 {
			secondaryStart := sample.SecondaryResetAt - sample.SecondaryWindowMinutes*60
			if startAt <= 0 || secondaryStart < startAt {
				startAt = secondaryStart
			}
		}
		if startAt <= 0 {
			startAt = sample.ObservedAt.Add(-quotaEstimatorHistoryWindow).Unix()
		}
		events, errEvents := s.quotaEstimatorEvents(sample.Account, time.Unix(startAt, 0), sample.ObservedAt)
		if errEvents != nil {
			return result, errEvents
		}
		account := QuotaEstimatorAccountOverview{
			Account: sample.Account, AuthID: sample.AuthID, AuthIndex: sample.AuthIndex,
			PlanType: sample.PlanType, LatestModel: sample.Model,
		}
		account.Primary = buildQuotaEstimatorWindow("primary", events, sample, false, prices, fastMultiplier, applyFast, missing)
		if sample.SecondaryUsedPercent != nil && sample.SecondaryResetAt > 0 && sample.SecondaryWindowMinutes > 0 {
			secondary := buildQuotaEstimatorWindow("weekly", events, sample, true, prices, fastMultiplier, applyFast, missing)
			account.Secondary = &secondary
		}
		result.Accounts = append(result.Accounts, account)
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		return result.Accounts[i].Primary.RemainingPercent < result.Accounts[j].Primary.RemainingPercent
	})
	for model := range missing {
		result.MissingPriceModels = append(result.MissingPriceModels, model)
	}
	sort.Strings(result.MissingPriceModels)
	return result, nil
}

func normalizeQuotaEstimatorPrices(custom map[string]QuotaEstimatorPrice) map[string]QuotaEstimatorPrice {
	prices := make(map[string]QuotaEstimatorPrice, len(defaultQuotaEstimatorPrices)+len(custom))
	for model, price := range defaultQuotaEstimatorPrices {
		prices[model] = price
	}
	for model, price := range custom {
		model = normalizeQuotaEstimatorModel(model)
		if model == "" || price.Prompt < 0 || price.Completion < 0 || price.Cache < 0 {
			continue
		}
		prices[model] = price
	}
	return prices
}

func normalizeQuotaEstimatorModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if index := strings.IndexByte(model, '('); index >= 0 {
		model = strings.TrimSpace(model[:index])
	}
	return model
}

func quotaEstimatorPriceForModel(prices map[string]QuotaEstimatorPrice, model string) (QuotaEstimatorPrice, bool) {
	model = normalizeQuotaEstimatorModel(model)
	if price, ok := prices[model]; ok {
		return price, true
	}
	bestKey := ""
	bestPrice := QuotaEstimatorPrice{}
	for key, price := range prices {
		if strings.HasPrefix(model, key+"-") && len(key) > len(bestKey) {
			bestKey = key
			bestPrice = price
		}
	}
	return bestPrice, bestKey != ""
}

func buildQuotaEstimatorWindow(scope string, events []quotaEstimatorEvent, latest quotaEstimatorEvent, secondary bool, prices map[string]QuotaEstimatorPrice, fastMultiplier float64, applyFast bool, missing map[string]struct{}) QuotaEstimatorWindowOverview {
	used := latest.PrimaryUsedPercent
	resetAt := latest.PrimaryResetAt
	windowMinutes := latest.PrimaryWindowMinutes
	if secondary {
		used = latest.SecondaryUsedPercent
		resetAt = latest.SecondaryResetAt
		windowMinutes = latest.SecondaryWindowMinutes
	}
	usedValue := 0.0
	if used != nil {
		usedValue = math.Max(0, math.Min(100, *used))
	}
	window := QuotaEstimatorWindowOverview{
		Scope: scope, UsedPercent: usedValue, RemainingPercent: math.Max(0, 100-usedValue),
		ResetAt: time.Unix(resetAt, 0).UTC(), WindowMinutes: windowMinutes,
		LastObservedAt: latest.ObservedAt.UTC(), Confidence: "insufficient", Status: "active",
	}
	if resetAt <= 0 || time.Now().Unix() >= resetAt {
		window.Status = "expired"
	}
	cycleStart := resetAt - windowMinutes*60
	points := make([]quotaEstimatorPoint, 0)
	var cumulativeTokens int64
	var cumulativeCost float64
	for _, event := range events {
		if event.ObservedAt.Unix() < cycleStart || event.ObservedAt.After(latest.ObservedAt) {
			continue
		}
		if !event.Failed || event.TotalTokens > 0 {
			cumulativeTokens += event.TotalTokens
			if price, ok := quotaEstimatorPriceForModel(prices, event.Model); ok {
				cumulativeCost += calculateQuotaEstimatorCost(event, price, fastMultiplier, applyFast)
			} else if strings.TrimSpace(event.Model) != "" {
				missing[event.Model] = struct{}{}
			}
		}
		pointUsed := event.PrimaryUsedPercent
		pointReset := event.PrimaryResetAt
		pointWindow := event.PrimaryWindowMinutes
		if secondary {
			pointUsed = event.SecondaryUsedPercent
			pointReset = event.SecondaryResetAt
			pointWindow = event.SecondaryWindowMinutes
		}
		if pointUsed == nil || pointReset != resetAt || pointWindow != windowMinutes {
			continue
		}
		points = append(points, quotaEstimatorPoint{ObservedAt: event.ObservedAt, Used: *pointUsed, Tokens: cumulativeTokens, Cost: cumulativeCost})
	}
	window.CurrentCycleTokens = cumulativeTokens
	window.CurrentCycleCostUSD = cumulativeCost
	applyQuotaEstimatorEstimate(&window, points)
	return window
}

func calculateQuotaEstimatorCost(event quotaEstimatorEvent, price QuotaEstimatorPrice, fastMultiplier float64, applyFast bool) float64 {
	cached := event.CachedTokens
	uncached := max(int64(0), event.InputTokens-cached)
	multiplier := 1.0
	if applyFast {
		tier := strings.ToLower(strings.TrimSpace(event.ServiceTier))
		if tier == "fast" || tier == "priority" || tier == "ultrafast" {
			multiplier = fastMultiplier
		}
	}
	return (float64(uncached)*price.Prompt + float64(cached)*price.Cache + float64(event.OutputTokens)*price.Completion) * multiplier / 1_000_000
}

func applyQuotaEstimatorEstimate(window *QuotaEstimatorWindowOverview, points []quotaEstimatorPoint) {
	if window == nil || len(points) < 2 {
		return
	}
	milestones := make([]quotaEstimatorPoint, 0, len(points))
	maxUsed := -1.0
	for _, point := range points {
		if point.Used > maxUsed {
			milestones = append(milestones, point)
			maxUsed = point.Used
		}
	}
	if len(milestones) < 2 {
		return
	}
	var tokenEstimates []float64
	var costEstimates []float64
	for index := 1; index < len(milestones); index++ {
		previous := milestones[index-1]
		current := milestones[index]
		deltaPercent := current.Used - previous.Used
		if deltaPercent <= 0 {
			continue
		}
		if deltaTokens := current.Tokens - previous.Tokens; deltaTokens > 0 {
			tokenEstimates = append(tokenEstimates, float64(deltaTokens)*100/deltaPercent)
		}
		if deltaCost := current.Cost - previous.Cost; deltaCost > 0 {
			costEstimates = append(costEstimates, deltaCost*100/deltaPercent)
		}
	}
	if len(tokenEstimates) == 0 || len(costEstimates) == 0 {
		return
	}
	window.EstimateAvailable = true
	window.FullWindowTokens = quotaEstimatorQuantile(tokenEstimates, 0.5)
	window.FullWindowCostUSD = quotaEstimatorQuantile(costEstimates, 0.5)
	window.CostLow = quotaEstimatorQuantile(costEstimates, 0.25)
	window.CostHigh = quotaEstimatorQuantile(costEstimates, 0.75)
	remainingRatio := math.Max(0, 100-window.UsedPercent) / 100
	window.RemainingTokens = window.FullWindowTokens * remainingRatio
	window.RemainingCostUSD = window.FullWindowCostUSD * remainingRatio
	window.SampleCount = min(len(tokenEstimates), len(costEstimates))
	window.PercentSpan = milestones[len(milestones)-1].Used - milestones[0].Used
	window.Confidence = "low"
	if window.SampleCount >= 5 && window.PercentSpan >= 5 {
		window.Confidence = "high"
	} else if window.SampleCount >= 2 && window.PercentSpan >= 2 {
		window.Confidence = "medium"
	}
}

func quotaEstimatorQuantile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	if len(ordered) == 1 {
		return ordered[0]
	}
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	return ordered[lower] + (ordered[upper]-ordered[lower])*(position-float64(lower))
}

func (s *sqliteDetailStore) latestQuotaEstimatorSamples() ([]quotaEstimatorEvent, error) {
	rows, err := s.db.Query(`SELECT id, requested_at_ns, observed_at_ns, account, auth_id, auth_index,
		model, service_tier, input_tokens, output_tokens, cached_tokens, cache_creation_tokens,
		total_tokens, failed, primary_used_percent, primary_reset_at, primary_window_minutes,
		secondary_used_percent, secondary_reset_at, secondary_window_minutes, plan_type
		FROM codex_quota_estimator_events
		WHERE primary_used_percent IS NOT NULL
			AND primary_reset_at > 0 AND primary_window_minutes > 0
			AND observed_at_ns >= ?
		ORDER BY observed_at_ns DESC, id DESC`, time.Now().Add(-quotaEstimatorHistoryWindow).UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("query quota estimator samples: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := make(map[string]struct{})
	var result []quotaEstimatorEvent
	for rows.Next() {
		event, errScan := scanQuotaEstimatorEvent(rows)
		if errScan != nil {
			return nil, errScan
		}
		if _, ok := seen[event.Account]; ok {
			continue
		}
		seen[event.Account] = struct{}{}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *sqliteDetailStore) quotaEstimatorEvents(account string, since, until time.Time) ([]quotaEstimatorEvent, error) {
	rows, err := s.db.Query(`SELECT id, requested_at_ns, observed_at_ns, account, auth_id, auth_index,
		model, service_tier, input_tokens, output_tokens, cached_tokens, cache_creation_tokens,
		total_tokens, failed, primary_used_percent, primary_reset_at, primary_window_minutes,
		secondary_used_percent, secondary_reset_at, secondary_window_minutes, plan_type
		FROM codex_quota_estimator_events
		WHERE account = ? AND observed_at_ns >= ? AND observed_at_ns <= ?
		ORDER BY observed_at_ns ASC, id ASC`, account, since.UTC().UnixNano(), until.UTC().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("query quota estimator events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []quotaEstimatorEvent
	for rows.Next() {
		event, errScan := scanQuotaEstimatorEvent(rows)
		if errScan != nil {
			return nil, errScan
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

type quotaEstimatorScanner interface {
	Scan(dest ...any) error
}

func scanQuotaEstimatorEvent(scanner quotaEstimatorScanner) (quotaEstimatorEvent, error) {
	var event quotaEstimatorEvent
	var requestedNS, observedNS int64
	var failed int
	var primaryUsed, secondaryUsed sql.NullFloat64
	err := scanner.Scan(&event.ID, &requestedNS, &observedNS, &event.Account, &event.AuthID, &event.AuthIndex,
		&event.Model, &event.ServiceTier, &event.InputTokens, &event.OutputTokens, &event.CachedTokens,
		&event.CacheCreationTokens, &event.TotalTokens, &failed, &primaryUsed, &event.PrimaryResetAt,
		&event.PrimaryWindowMinutes, &secondaryUsed, &event.SecondaryResetAt, &event.SecondaryWindowMinutes,
		&event.PlanType)
	if err != nil {
		return event, fmt.Errorf("scan quota estimator event: %w", err)
	}
	event.RequestedAt = time.Unix(0, requestedNS).UTC()
	event.ObservedAt = time.Unix(0, observedNS).UTC()
	event.Failed = failed != 0
	if primaryUsed.Valid {
		value := primaryUsed.Float64
		event.PrimaryUsedPercent = &value
	}
	if secondaryUsed.Valid {
		value := secondaryUsed.Float64
		event.SecondaryUsedPercent = &value
	}
	return event, nil
}
