package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry stores a downstream API key and its optional management remark.
type APIKeyEntry struct {
	APIKey string `yaml:"api-key" json:"api-key"`
	Remark string `yaml:"remark,omitempty" json:"remark,omitempty"`
}

// APIKeyEntryList accepts both legacy string entries and structured entries.
type APIKeyEntryList []APIKeyEntry

// UnmarshalYAML keeps api-keys backward compatible with legacy string lists.
func (l *APIKeyEntryList) UnmarshalYAML(value *yaml.Node) error {
	if l == nil {
		return nil
	}
	if value == nil || value.Kind == 0 {
		*l = nil
		return nil
	}
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("api-keys must be a list")
	}

	entries := make(APIKeyEntryList, 0, len(value.Content))
	for _, node := range value.Content {
		entry, err := apiKeyEntryFromYAMLNode(node)
		if err != nil {
			return err
		}
		entries = append(entries, entry)
	}
	*l = entries
	return nil
}

// MarshalYAML writes legacy string lists when no remarks are configured.
func (l APIKeyEntryList) MarshalYAML() (any, error) {
	entries := normalizeAPIKeyEntries(l)
	if len(entries) == 0 {
		return []string{}, nil
	}
	hasRemark := false
	for _, entry := range entries {
		if strings.TrimSpace(entry.Remark) != "" {
			hasRemark = true
			break
		}
	}
	if !hasRemark {
		keys := make([]string, 0, len(entries))
		for _, entry := range entries {
			keys = append(keys, entry.APIKey)
		}
		return keys, nil
	}
	return []APIKeyEntry(entries), nil
}

// UnmarshalJSON accepts both string arrays and structured arrays.
func (l *APIKeyEntryList) UnmarshalJSON(data []byte) error {
	if l == nil {
		return nil
	}
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	entries := make(APIKeyEntryList, 0, len(raw))
	for _, item := range raw {
		entry, err := apiKeyEntryFromJSONValue(item)
		if err != nil {
			return err
		}
		entries = append(entries, entry)
	}
	*l = entries
	return nil
}

func apiKeyEntryFromYAMLNode(node *yaml.Node) (APIKeyEntry, error) {
	if node == nil {
		return APIKeyEntry{}, nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		var value string
		if err := node.Decode(&value); err != nil {
			return APIKeyEntry{}, fmt.Errorf("parse api key entry: %w", err)
		}
		return APIKeyEntry{APIKey: value}, nil
	case yaml.MappingNode:
		entry := APIKeyEntry{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			if keyNode == nil || valueNode == nil {
				continue
			}
			var value string
			if err := valueNode.Decode(&value); err != nil {
				return APIKeyEntry{}, fmt.Errorf("parse api key %q: %w", keyNode.Value, err)
			}
			switch normalizeAPIKeyEntryField(keyNode.Value) {
			case "api-key":
				entry.APIKey = value
			case "remark":
				entry.Remark = value
			}
		}
		return entry, nil
	default:
		return APIKeyEntry{}, fmt.Errorf("api key entries must be strings or mappings")
	}
}

func apiKeyEntryFromJSONValue(value any) (APIKeyEntry, error) {
	switch item := value.(type) {
	case string:
		return APIKeyEntry{APIKey: item}, nil
	case map[string]any:
		entry := APIKeyEntry{}
		for key, rawValue := range item {
			valueString, ok := rawValue.(string)
			if !ok {
				continue
			}
			switch normalizeAPIKeyEntryField(key) {
			case "api-key":
				entry.APIKey = valueString
			case "remark":
				entry.Remark = valueString
			}
		}
		return entry, nil
	default:
		return APIKeyEntry{}, fmt.Errorf("api key entries must be strings or objects")
	}
}

func normalizeAPIKeyEntryField(field string) string {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "api-key", "api_key", "apikey", "api key", "key", "value":
		return "api-key"
	case "remark", "remarks", "note", "notes", "name", "label":
		return "remark"
	default:
		return ""
	}
}

func normalizeAPIKeyEntries(entries APIKeyEntryList) APIKeyEntryList {
	if len(entries) == 0 {
		return nil
	}
	out := make(APIKeyEntryList, 0, len(entries))
	seen := make(map[string]int, len(entries))
	for _, entry := range entries {
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		entry.Remark = strings.TrimSpace(entry.Remark)
		if entry.APIKey == "" {
			continue
		}
		if existingIndex, exists := seen[entry.APIKey]; exists {
			if out[existingIndex].Remark == "" && entry.Remark != "" {
				out[existingIndex].Remark = entry.Remark
			}
			continue
		}
		seen[entry.APIKey] = len(out)
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func apiKeyEntriesFromKeys(keys []string) APIKeyEntryList {
	if len(keys) == 0 {
		return nil
	}
	entries := make(APIKeyEntryList, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, APIKeyEntry{APIKey: key})
	}
	return normalizeAPIKeyEntries(entries)
}

// SanitizeAPIKeyEntries normalizes downstream API key entries and updates APIKeys.
func (cfg *SDKConfig) SanitizeAPIKeyEntries() {
	if cfg == nil {
		return
	}
	entries := normalizeAPIKeyEntries(cfg.APIKeyEntries)
	if len(entries) == 0 && len(cfg.APIKeys) > 0 {
		entries = apiKeyEntriesFromKeys(cfg.APIKeys)
	}
	cfg.APIKeyEntries = entries
	cfg.APIKeys = cfg.APIKeyStrings()
}

// APIKeyStrings returns the normalized downstream API key values.
func (cfg *SDKConfig) APIKeyStrings() []string {
	if cfg == nil {
		return nil
	}
	entries := normalizeAPIKeyEntries(cfg.APIKeyEntries)
	if len(entries) == 0 {
		entries = apiKeyEntriesFromKeys(cfg.APIKeys)
	}
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.APIKey)
	}
	return keys
}

// APIKeyRemarkMap returns remarks keyed by downstream API key.
func (cfg *SDKConfig) APIKeyRemarkMap() map[string]string {
	if cfg == nil {
		return nil
	}
	entries := normalizeAPIKeyEntries(cfg.APIKeyEntries)
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.Remark != "" {
			out[entry.APIKey] = entry.Remark
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
