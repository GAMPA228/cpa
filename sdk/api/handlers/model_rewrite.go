package handlers

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

func (h *BaseAPIHandler) applyModelRewrite(ctx context.Context, handlerType, modelName string, rawJSON []byte) (string, []byte) {
	if h == nil || h.Cfg == nil || !isOpenAICompatibleModelRewriteProtocol(handlerType) {
		return modelName, rawJSON
	}
	rewriteResult, ok := h.Cfg.RewriteModelForAPIKeyWithOptions(userAPIKeyFromExecutionContext(ctx), modelName)
	if !ok {
		return modelName, rawJSON
	}
	rewriteModel := rewriteResult.Model
	if len(rawJSON) == 0 {
		return rewriteModel, rawJSON
	}
	// Image executors apply the routed model when serializing multipart uploads.
	// sjson accepts non-JSON input and would silently discard all uploaded files.
	if !json.Valid(rawJSON) {
		return rewriteModel, rawJSON
	}
	rewrittenJSON, err := sjson.SetBytes(rawJSON, "model", rewriteModel)
	if err != nil {
		return modelName, rawJSON
	}
	if rewriteResult.ThinkingEffort != "" {
		rewrittenJSON, err = setModelRewriteThinkingEffort(rewrittenJSON, handlerType, rewriteResult.ThinkingEffort)
		if err != nil {
			return modelName, rawJSON
		}
	}
	return rewriteModel, rewrittenJSON
}

func isOpenAICompatibleModelRewriteProtocol(handlerType string) bool {
	handlerType = strings.ToLower(strings.TrimSpace(handlerType))
	return handlerType == "openai" || strings.HasPrefix(handlerType, "openai-")
}

func setModelRewriteThinkingEffort(rawJSON []byte, handlerType, effort string) ([]byte, error) {
	handlerType = strings.ToLower(strings.TrimSpace(handlerType))
	effort = strings.ToLower(strings.TrimSpace(effort))
	if strings.Contains(handlerType, "response") {
		return sjson.SetBytes(rawJSON, "reasoning.effort", effort)
	}
	return sjson.SetBytes(rawJSON, "reasoning_effort", effort)
}

func userAPIKeyFromExecutionContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	raw, exists := ginCtx.Get("userApiKey")
	if !exists {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}
