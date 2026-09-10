package helps

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	codexclient "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var codexIdentityKeys = struct {
	sync.Mutex
	values map[string][]byte
}{values: make(map[string][]byte)}

// Load once per auth directory. A missing or invalid key must never cause a
// request to silently fall back to an unmasked or newly randomized identity.
func codexIdentityKey(authDir string) ([]byte, error) {
	if strings.TrimSpace(authDir) == "" {
		return nil, fmt.Errorf("codex single-device requires auth-dir")
	}
	dir, err := util.ResolveAuthDir(authDir)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".codex-identity-key")
	codexIdentityKeys.Lock()
	defer codexIdentityKeys.Unlock()
	if key := codexIdentityKeys.values[path]; key != nil {
		return key, nil
	}
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create codex identity directory: %w", err)
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		var file *os.File
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			key, err = os.ReadFile(path)
		} else if err == nil {
			_, err = file.Write(key)
			if err == nil {
				err = file.Sync()
			}
			errClose := file.Close()
			if err == nil {
				err = errClose
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("persist codex identity key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid codex identity key; restore auth-dir/.codex-identity-key from backup")
	}
	codexIdentityKeys.values[path] = key
	return key, nil
}

// CodexIdentity is local to one upstream attempt, never a shared session object.
type CodexIdentity struct {
	key       []byte
	principal string
	device    string
	codex     bool
	modern    bool
	headers   http.Header
	reverse   map[string]string
}

func identityHeader(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func identityDigest(key []byte, parts ...string) []byte {
	raw, _ := json.Marshal(parts)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return mac.Sum(nil)
}

func (s *CodexIdentity) alias(kind, value string) string {
	if value == "" {
		return ""
	}
	raw := identityDigest(s.key, kind, s.principal, value)
	var id uuid.UUID
	copy(id[:], raw[:16])
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	result := id.String()
	s.reverse[result] = value
	return result
}

var codexIdentityHeaderKinds = map[string]string{
	"Session-Id": "session", "session_id": "session", "Conversation_id": "session",
	"Thread-Id": "session", "X-Codex-Window-Id": "window",
	"X-Client-Request-Id": "request",
}

var codexIdentityMetadataKinds = map[string]string{
	"session_id": "session", "thread_id": "session", "parent_thread_id": "session",
	"parent_session_id": "session", "forked_from_thread_id": "session",
	"window_id": "window", "turn_id": "turn", "prompt_cache_key": "cache",
}

// PrepareCodexIdentity uses the original client headers, before proxy cloaking.
// Account IDs survive token rotation; credential ID is a fallback for older files.
func PrepareCodexIdentity(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, headers http.Header, body []byte) ([]byte, *CodexIdentity, error) {
	if cfg == nil || cfg.Codex.SingleDevice == nil || !*cfg.Codex.SingleDevice || auth == nil || auth.AuthKind() != cliproxyauth.AuthKindOAuth {
		return body, nil, nil
	}
	if ctx != nil {
		if c, ok := ctx.Value("gin").(*gin.Context); ok && c != nil && c.Request != nil {
			headers = c.Request.Header
		}
	}
	master, err := codexIdentityKey(cfg.AuthDir)
	if err != nil {
		return nil, nil, err
	}
	account, _ := auth.Metadata["account_id"].(string)
	account = strings.TrimSpace(account)
	if account == "" {
		account = strings.TrimSpace(auth.ID)
	}
	if account == "" {
		return nil, nil, fmt.Errorf("codex single-device requires a stable account ID")
	}
	s := &CodexIdentity{
		key:       identityDigest(master, "codex-account", account),
		principal: APIKeyFromContext(ctx), headers: headers.Clone(), reverse: make(map[string]string),
	}
	ua := identityHeader(headers, "User-Agent")
	s.codex = codexclient.IsCodexClientUserAgent(ua)
	// Only modern Codex identities opt out of the historical underscore headers.
	for _, prefix := range []string{"codex-tui/", "codex_cli_rs/", "codex_vscode/"} {
		if i := strings.Index(strings.ToLower(ua), prefix); i >= 0 {
			var major, minor int
			if _, errScan := fmt.Sscanf(ua[i+len(prefix):], "%d.%d", &major, &minor); errScan == nil {
				s.modern = major > 0 || minor >= 153
			}
		}
	}
	// Device scope deliberately excludes the downstream principal and installation.
	device := identityDigest(s.key, "device")
	var deviceID uuid.UUID
	copy(deviceID[:], device[:16])
	deviceID[6] = (deviceID[6] & 0x0f) | 0x80
	deviceID[8] = (deviceID[8] & 0x3f) | 0x80
	s.device = deviceID.String()
	if value := gjson.GetBytes(body, "prompt_cache_key"); value.Type == gjson.String && value.Str != "" {
		body = SetStringIfDifferent(body, "prompt_cache_key", s.alias("cache", value.Str))
	}
	if s.codex {
		if original := gjson.GetBytes(body, "client_metadata.x-codex-installation-id"); original.Type == gjson.String && original.Str != "" {
			s.reverse[s.device] = original.Str
		}
		body = SetStringIfDifferent(body, "client_metadata.x-codex-installation-id", s.device)
		for header, kind := range codexIdentityHeaderKinds {
			path := "client_metadata." + strings.ToLower(header)
			if value := gjson.GetBytes(body, path); value.Type == gjson.String && value.Str != "" {
				body = SetStringIfDifferent(body, path, s.alias(kind, value.Str))
			}
		}
		if value := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata"); value.Type == gjson.String {
			body = SetStringIfDifferent(body, "client_metadata.x-codex-turn-metadata", s.turnMetadata(value.Str))
		}
		if s.modern {
			for _, name := range []string{"session_id", "conversation_id"} {
				body, _ = sjson.DeleteBytes(body, "client_metadata."+name)
			}
		}
	}
	return body, s, nil
}

func (s *CodexIdentity) turnMetadata(raw string) string {
	if !gjson.Valid(raw) || !gjson.Parse(raw).IsObject() {
		return raw
	}
	for key, kind := range codexIdentityMetadataKinds {
		if value := gjson.Get(raw, key); value.Type == gjson.String && value.Str != "" {
			raw, _ = sjson.Set(raw, key, s.alias(kind, value.Str))
		}
	}
	return raw
}

// ApplyHeaders removes proxy-generated window identities for non-Codex clients.
func (s *CodexIdentity) ApplyHeaders(headers http.Header) {
	if s == nil {
		return
	}
	for name, kind := range codexIdentityHeaderKinds {
		value := identityHeader(s.headers, name)
		for key := range headers {
			if strings.EqualFold(key, name) {
				delete(headers, key)
			}
		}
		if s.codex {
			if s.modern && strings.Contains(name, "_") {
				continue
			}
			value = s.alias(kind, value)
		}
		if value != "" {
			headers.Set(name, value)
		}
	}
	if s.codex {
		headers.Set("X-Codex-Installation-Id", s.device)
		if raw := identityHeader(s.headers, "X-Codex-Turn-Metadata"); raw != "" {
			headers.Set("X-Codex-Turn-Metadata", s.turnMetadata(raw))
		}
	}
}

// RestoreResponse only restores known metadata fields, never generated text or
// upstream response IDs (which are required for previous_response_id continuity).
func (s *CodexIdentity) RestoreResponse(payload []byte) []byte {
	if s == nil {
		return payload
	}
	if gjson.ValidBytes(payload) {
		return s.restoreResponseJSON(payload)
	}
	lines := bytes.Split(payload, []byte("\n"))
	for i, line := range lines {
		prefix := []byte(nil)
		raw := line
		if bytes.HasPrefix(line, []byte("data:")) {
			prefix = []byte("data:")
			raw = line[len(prefix):]
		}
		if !gjson.ValidBytes(raw) {
			continue
		}
		raw = s.restoreResponseJSON(raw)
		lines[i] = append(append([]byte(nil), prefix...), raw...)
	}
	return bytes.Join(lines, []byte("\n"))
}

func (s *CodexIdentity) restoreResponseJSON(raw []byte) []byte {
	for _, root := range []string{"", "response."} {
		paths := []string{root + "prompt_cache_key", root + "client_metadata.x-codex-installation-id"}
		for header := range codexIdentityHeaderKinds {
			paths = append(paths, root+"client_metadata."+strings.ToLower(header))
		}
		for _, path := range paths {
			if original, ok := s.reverse[gjson.GetBytes(raw, path).String()]; ok {
				raw = SetStringIfDifferent(raw, path, original)
			}
		}
		path := root + "client_metadata.x-codex-turn-metadata"
		value := gjson.GetBytes(raw, path)
		if value.Type != gjson.String || !gjson.Valid(value.Str) {
			continue
		}
		metadata := value.Str
		for name := range codexIdentityMetadataKinds {
			if original, ok := s.reverse[gjson.Get(metadata, name).String()]; ok {
				metadata, _ = sjson.Set(metadata, name, original)
			}
		}
		raw = SetStringIfDifferent(raw, path, metadata)
	}
	return raw
}
