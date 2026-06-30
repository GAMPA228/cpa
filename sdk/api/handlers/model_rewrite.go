package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

func (h *BaseAPIHandler) applyModelRewrite(ctx context.Context, handlerType, modelName string, rawJSON []byte) (string, []byte) {
	if h == nil || h.Cfg == nil || !isOpenAICompatibleModelRewriteProtocol(handlerType) {
		return modelName, rawJSON
	}
	rewriteModel, ok := h.Cfg.RewriteModelForAPIKey(userAPIKeyFromExecutionContext(ctx), modelName)
	if !ok {
		return modelName, rawJSON
	}
	if len(rawJSON) == 0 {
		return rewriteModel, rawJSON
	}
	rewrittenJSON, err := sjson.SetBytes(rawJSON, "model", rewriteModel)
	if err != nil {
		return modelName, rawJSON
	}
	return rewriteModel, rewrittenJSON
}

func isOpenAICompatibleModelRewriteProtocol(handlerType string) bool {
	handlerType = strings.ToLower(strings.TrimSpace(handlerType))
	return handlerType == "openai" || strings.HasPrefix(handlerType, "openai-")
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
