package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

const modelRewriteServiceUnavailableBody = `{"error":{"message":"服务暂时不可用，请稍后重试；如持续失败，请联系管理员","type":"server_error","code":"service_unavailable"}}`

var modelRewriteResponseModelPaths = []string{
	"model",
	"modelVersion",
	"response.model",
	"response.modelVersion",
	"message.model",
}

func modelRewriteApplied(requestedModel, rewrittenModel string) bool {
	return strings.TrimSpace(requestedModel) != "" && strings.TrimSpace(rewrittenModel) != "" && requestedModel != rewrittenModel
}

func cloakModelRewriteResponse(payload []byte, requestedModel, effectiveModel string, enabled bool) []byte {
	requestedModel = strings.TrimSpace(requestedModel)
	if !enabled || requestedModel == "" || len(payload) == 0 {
		return payload
	}

	trimmed := bytes.TrimSpace(payload)
	if json.Valid(trimmed) {
		return rewriteModelRewriteJSON(trimmed, requestedModel, effectiveModel)
	}

	lines := bytes.Split(payload, []byte("\n"))
	changed := false
	for i, line := range lines {
		trimmedLine := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(trimmedLine[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || !json.Valid(data) {
			continue
		}
		prefixOffset := bytes.Index(line, []byte("data:"))
		if prefixOffset < 0 {
			continue
		}
		prefixEnd := prefixOffset + len("data:")
		for prefixEnd < len(line) && (line[prefixEnd] == ' ' || line[prefixEnd] == '\t') {
			prefixEnd++
		}
		rewritten := rewriteModelRewriteJSON(data, requestedModel, effectiveModel)
		lines[i] = append(append([]byte{}, line[:prefixEnd]...), rewritten...)
		changed = true
	}
	if !changed {
		return payload
	}
	return bytes.Join(lines, []byte("\n"))
}

func rewriteModelRewriteJSON(payload []byte, requestedModel, effectiveModel string) []byte {
	result := payload
	if isModelRewriteErrorPayload(result) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(result))
		decoder.UseNumber()
		if errDecode := decoder.Decode(&value); errDecode == nil {
			value = sanitizeModelRewriteErrorJSONValue(value, requestedModel, effectiveModel)
			if encoded, errMarshal := json.Marshal(value); errMarshal == nil {
				result = encoded
			}
		}
	}
	for _, path := range modelRewriteResponseModelPaths {
		if !gjson.GetBytes(result, path).Exists() {
			continue
		}
		updated, errSet := sjson.SetBytes(result, path, requestedModel)
		if errSet == nil {
			result = updated
		}
	}
	return result
}

func isModelRewriteErrorPayload(payload []byte) bool {
	eventType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "type").String()))
	if strings.Contains(eventType, "error") || strings.Contains(eventType, "failed") {
		return true
	}
	for _, path := range []string{"error", "response.error"} {
		value := gjson.GetBytes(payload, path)
		if value.Exists() && value.Type != gjson.Null {
			return true
		}
	}
	return false
}

func sanitizeModelRewriteErrorMessage(ctx context.Context, msg *interfaces.ErrorMessage, requestedModel, effectiveModel string, enabled bool) *interfaces.ErrorMessage {
	if !enabled || msg == nil || msg.DirectResponse {
		return msg
	}

	status := msg.StatusCode
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if isAuthSelectionUnavailable(msg.Error) || status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden || status >= http.StatusInternalServerError {
		logHiddenModelRewriteError(ctx, msg.Error, status, requestedModel, effectiveModel)
		publicMsg := cloneModelRewriteErrorMessage(msg)
		publicMsg.StatusCode = http.StatusServiceUnavailable
		publicMsg.Error = errors.New(modelRewriteServiceUnavailableBody)
		publicMsg.Addon = retryAfterOnlyHeader(msg.Addon)
		return publicMsg
	}

	if msg.Error == nil {
		return msg
	}
	publicText := sanitizeModelRewriteErrorText(msg.Error.Error(), requestedModel, effectiveModel)
	if publicText == msg.Error.Error() {
		return msg
	}
	logHiddenModelRewriteError(ctx, msg.Error, status, requestedModel, effectiveModel)
	publicMsg := cloneModelRewriteErrorMessage(msg)
	publicMsg.Error = errors.New(publicText)
	return publicMsg
}

func cloneModelRewriteErrorMessage(msg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if msg == nil {
		return nil
	}
	cloned := *msg
	cloned.Body = cloneBytes(msg.Body)
	cloned.Headers = cloneHeader(msg.Headers)
	cloned.Addon = cloneHeader(msg.Addon)
	return &cloned
}

func retryAfterOnlyHeader(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	retryAfter := strings.TrimSpace(headers.Get("Retry-After"))
	if retryAfter == "" {
		return nil
	}
	return http.Header{"Retry-After": []string{retryAfter}}
}

func sanitizeModelRewriteErrorText(value, requestedModel, effectiveModel string) string {
	value = replaceEffectiveModelReferences(value, requestedModel, effectiveModel)
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return value
	}

	var payload any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		return value
	}
	payload = sanitizeModelRewriteErrorJSONValue(payload, requestedModel, effectiveModel)
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return value
	}
	return string(encoded)
}

func sanitizeModelRewriteErrorJSONValue(value any, requestedModel, effectiveModel string) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
			if isModelRewriteSensitiveErrorKey(normalizedKey) {
				delete(typed, key)
				continue
			}
			if normalizedKey == "model" || normalizedKey == "model_name" || normalizedKey == "modelversion" || normalizedKey == "model_version" {
				typed[key] = requestedModel
				continue
			}
			typed[key] = sanitizeModelRewriteErrorJSONValue(child, requestedModel, effectiveModel)
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = sanitizeModelRewriteErrorJSONValue(typed[i], requestedModel, effectiveModel)
		}
		return typed
	case string:
		return replaceEffectiveModelReferences(typed, requestedModel, effectiveModel)
	default:
		return value
	}
}

func isModelRewriteSensitiveErrorKey(key string) bool {
	switch key {
	case "provider", "providers", "auth", "auth_id", "auth_index", "credential_id", "account_id", "url", "upstream_url", "base_url":
		return true
	default:
		return false
	}
}

func replaceEffectiveModelReferences(value, requestedModel, effectiveModel string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return value
	}
	candidates := []string{strings.TrimSpace(effectiveModel)}
	if baseModel := strings.TrimSpace(thinking.ParseSuffix(effectiveModel).ModelName); baseModel != "" {
		candidates = append(candidates, baseModel)
		if routedBaseModel := routeModelBaseName(baseModel); routedBaseModel != "" {
			candidates = append(candidates, routedBaseModel)
		}
	}
	for _, candidate := range candidates {
		if candidate == "" || candidate == requestedModel {
			continue
		}
		value = strings.ReplaceAll(value, candidate, requestedModel)
	}
	return value
}

func logHiddenModelRewriteError(ctx context.Context, err error, status int, requestedModel, effectiveModel string) {
	fields := log.Fields{
		"status":          status,
		"requested_model": requestedModel,
		"effective_model": effectiveModel,
	}
	if requestID := logging.GetRequestID(ctx); requestID != "" {
		fields["request_id"] = requestID
	}
	entry := log.WithFields(fields)
	if err != nil {
		entry = entry.WithError(err)
	}
	entry.Warn("model rewrite: hid internal execution details from downstream response")
}
