package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authheaders"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type headerRuleAccount struct {
	AuthID   string             `json:"auth_id"`
	Name     string             `json:"name"`
	Note     string             `json:"note"`
	Models   []string           `json:"models"`
	Rules    []authheaders.Rule `json:"rules"`
	Revision string             `json:"revision"`
}

type headerRuleDraft struct {
	Name            string   `json:"name"`
	Operation       string   `json:"operation"`
	Value           string   `json:"value"`
	Models          []string `json:"models"`
	DurationMinutes int      `json:"duration_minutes"`
}

type headerRuleMutation struct {
	AuthID   string          `json:"auth_id"`
	Revision string          `json:"revision"`
	Action   string          `json:"action"`
	ID       string          `json:"id"`
	Rule     headerRuleDraft `json:"rule"`
}

func editableHeaderRuleAccount(auth *coreauth.Auth) bool {
	return auth != nil && strings.EqualFold(auth.Provider, "codex") &&
		auth.FileName != "" && !coreauth.IsPluginVirtualAuth(auth) && !isRuntimeOnlyAuth(auth)
}

func headerRulesRevision(rules []authheaders.Rule) string {
	data, _ := json.Marshal(rules)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (h *Handler) buildHeaderRuleAccount(auth *coreauth.Auth) (headerRuleAccount, error) {
	rules, err := authheaders.Decode(auth.Metadata[authheaders.MetadataKey])
	if err != nil {
		return headerRuleAccount{}, err
	}
	if rules == nil {
		rules = []authheaders.Rule{}
	}
	name := auth.FileName
	note, _ := auth.Metadata["note"].(string)
	models := make(map[string]bool)
	// Include canonical definitions as registered models can be downstream aliases.
	for _, model := range registry.GetStaticModelDefinitionsByChannel("codex") {
		models[model.ID] = true
	}
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		upstream := h.authManager.ResolveExecutionModel(auth, model.ID)
		models[thinking.ParseSuffix(upstream).ModelName] = true
	}
	for _, rule := range rules {
		for _, model := range rule.Models {
			models[model] = true
		}
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	return headerRuleAccount{AuthID: auth.ID, Name: name, Note: note, Models: names, Rules: rules, Revision: headerRulesRevision(rules)}, nil
}

func (h *Handler) GetRequestHeaderRules(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	accounts := make([]headerRuleAccount, 0)
	for _, auth := range h.authManager.List() {
		if !editableHeaderRuleAccount(auth) {
			continue
		}
		account, err := h.buildHeaderRuleAccount(auth)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid account header rules", "auth_id": auth.ID})
			return
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	c.JSON(http.StatusOK, gin.H{"accounts": accounts, "server_time": time.Now().UTC(), "read_only": !h.headerRulesWritable()})
}

func (h *Handler) headerRulesWritable() bool {
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	return host.HasCodexHeaderPlugin()
}

// mutateHeaderRules changes exactly one rule. Existing absolute deadlines are
// retained unless duration changes or an explicit restart is requested.
func mutateHeaderRules(rules []authheaders.Rule, input headerRuleMutation, now time.Time) ([]authheaders.Rule, error) {
	index := -1
	for i := range rules {
		if rules[i].ID == input.ID {
			index = i
			break
		}
	}
	if input.ID != "" && index < 0 {
		return nil, fmt.Errorf("rule no longer exists")
	}
	next := append([]authheaders.Rule(nil), rules...)
	switch input.Action {
	case "delete":
		if index < 0 {
			return nil, fmt.Errorf("rule ID is required")
		}
		next = append(next[:index], next[index+1:]...)
	case "restart":
		if index < 0 || next[index].DurationMinutes == 0 {
			return nil, fmt.Errorf("only temporary rules can be restarted")
		}
		expiry := now.Add(time.Duration(next[index].DurationMinutes) * time.Minute).UTC()
		next[index].ExpiresAt = &expiry
	case "save":
		draft := input.Rule
		if !authheaders.ValidDuration(draft.DurationMinutes) {
			return nil, fmt.Errorf("invalid duration")
		}
		rule := authheaders.Rule{ID: input.ID, Name: draft.Name, Operation: draft.Operation, Value: draft.Value, Models: draft.Models, DurationMinutes: draft.DurationMinutes}
		if index < 0 {
			rule.ID = uuid.NewString()
		} else {
			rule.ExpiresAt = next[index].ExpiresAt
		}
		if draft.DurationMinutes == 0 {
			rule.ExpiresAt = nil
		} else if index < 0 || next[index].DurationMinutes != draft.DurationMinutes {
			expiry := now.Add(time.Duration(draft.DurationMinutes) * time.Minute).UTC()
			rule.ExpiresAt = &expiry
		}
		if index < 0 {
			next = append(next, rule)
		} else {
			next[index] = rule
		}
	default:
		return nil, fmt.Errorf("invalid action")
	}
	return authheaders.Decode(next)
}

func (h *Handler) MutateRequestHeaderRules(c *gin.Context) {
	if !h.headerRulesWritable() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex header rules are read-only while the header plugin is unavailable", "read_only": true})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var input headerRuleMutation
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule request"})
		return
	}
	h.headerRulesMu.Lock()
	defer h.headerRulesMu.Unlock()
	auth, ok := h.authManager.GetByID(input.AuthID)
	if !ok || !editableHeaderRuleAccount(auth) {
		c.JSON(http.StatusNotFound, gin.H{"error": "editable Codex account not found"})
		return
	}
	current, err := h.buildHeaderRuleAccount(auth)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Revision == "" || input.Revision != current.Revision {
		c.JSON(http.StatusConflict, gin.H{"error": "rules changed; refresh before saving"})
		return
	}
	rules, err := mutateHeaderRules(current.Rules, input, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	if rules == nil {
		rules = []authheaders.Rule{}
	}
	// JSON-native metadata is required by existing clone, merge and token stores.
	data, _ := json.Marshal(rules)
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		c.JSON(500, gin.H{"error": "cannot encode rules"})
		return
	}
	updated.Metadata[authheaders.MetadataKey] = value
	saved, err := h.authManager.UpdatePreparedAuthPersisted(c.Request.Context(), auth, updated)
	if err != nil || saved == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist rules; refresh to check runtime state"})
		return
	}
	if h.postAuthPersistHook != nil {
		if err := h.postAuthPersistHook(c.Request.Context(), saved); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to synchronize rules; refresh before retrying"})
			return
		}
	}
	account, err := h.buildHeaderRuleAccount(saved)
	if err != nil {
		c.JSON(500, gin.H{"error": "cannot load saved rules"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"account": account, "server_time": time.Now().UTC()})
}
