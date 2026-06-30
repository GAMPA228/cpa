package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestExecuteWithAuthManagerRewritesOpenAIModelForNonWhitelistedAPIKey(t *testing.T) {
	sourceModel := "gpt-5.5"
	targetModel := "gpt-5.4"
	executor := &modelExecutionCaptureExecutor{}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[]}`, sourceModel))
	resp, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(resp) != "model-execution-ok" {
		t.Fatalf("response = %q, want model-execution-ok", resp)
	}

	gotReq, gotOpts := executor.captured()
	assertModelRewriteApplied(t, gotReq, gotOpts, sourceModel, targetModel)
}

func TestExecuteWithAuthManagerKeepsWhitelistedAPIKeyModel(t *testing.T) {
	sourceModel := "gpt-5.5"
	targetModel := "gpt-5.4"
	executor := &modelExecutionCaptureExecutor{}
	handler := newModelExecutionHandler(t, sourceModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[]}`, sourceModel))
	_, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-whitelist"), "openai", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}

	gotReq, gotOpts := executor.captured()
	if gotReq.Model != sourceModel {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, sourceModel)
	}
	if got := gjson.GetBytes(gotReq.Payload, "model").String(); got != sourceModel {
		t.Fatalf("payload model = %q, want %q", got, sourceModel)
	}
	if gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey] != sourceModel {
		t.Fatalf("requested model metadata = %#v, want %q", gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey], sourceModel)
	}
}

func TestExecuteWithAuthManagerDoesNotRewriteNonOpenAIProtocol(t *testing.T) {
	sourceModel := "gpt-5.5"
	targetModel := "gpt-5.4"
	executor := &modelExecutionCaptureExecutor{}
	handler := newModelExecutionHandler(t, sourceModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"contents":[]}`, sourceModel))
	_, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "gemini", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}

	gotReq, _ := executor.captured()
	if gotReq.Model != sourceModel {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, sourceModel)
	}
	if got := gjson.GetBytes(gotReq.Payload, "model").String(); got != sourceModel {
		t.Fatalf("payload model = %q, want %q", got, sourceModel)
	}
}

func TestExecuteCountWithAuthManagerRewritesOpenAIModel(t *testing.T) {
	sourceModel := "gpt-5.5"
	targetModel := "gpt-5.4"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q}`, sourceModel))
	_, _, errMsg := handler.ExecuteCountWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteCountWithAuthManager() error = %+v", errMsg)
	}

	gotReq, gotOpts := executor.captured()
	assertModelRewriteApplied(t, gotReq, gotOpts, sourceModel, targetModel)
}

func TestExecuteStreamWithAuthManagerRewritesOpenAIModel(t *testing.T) {
	sourceModel := "gpt-5.5"
	targetModel := "gpt-5.4"
	executor := &modelExecutionCaptureExecutor{
		stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("data: ok\n\n")}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, sourceModel))
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai", sourceModel, body, "")
	gotChunk := false
	for chunk := range dataChan {
		gotChunk = true
		if string(chunk) != "data: ok\n\n" {
			t.Fatalf("stream chunk = %q, want data: ok", chunk)
		}
	}
	for errMsg := range errChan {
		if errMsg != nil {
			t.Fatalf("ExecuteStreamWithAuthManager() error = %+v", errMsg)
		}
	}
	if !gotChunk {
		t.Fatal("stream produced no chunk")
	}

	gotReq, gotOpts := executor.captured()
	assertModelRewriteApplied(t, gotReq, gotOpts, sourceModel, targetModel)
}

func modelRewriteTestConfig(sourceModel, targetModel string) *sdkconfig.SDKConfig {
	return &sdkconfig.SDKConfig{
		ModelRewrite: sdkconfig.ModelRewriteConfig{
			Enabled: true,
			Rules: []sdkconfig.ModelRewriteRule{
				{
					MatchModels:   []string{sourceModel},
					TargetModel:   targetModel,
					BypassAPIKeys: []string{"sk-whitelist"},
				},
			},
		},
	}
}

func modelRewriteContextWithAPIKey(t *testing.T, apiKey string) context.Context {
	t.Helper()
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Set("userApiKey", apiKey)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func assertModelRewriteApplied(t *testing.T, req coreexecutor.Request, opts coreexecutor.Options, sourceModel, targetModel string) {
	t.Helper()
	if req.Model != targetModel {
		t.Fatalf("executor model = %q, want %q", req.Model, targetModel)
	}
	if got := gjson.GetBytes(req.Payload, "model").String(); got != targetModel {
		t.Fatalf("payload model = %q, want %q", got, targetModel)
	}
	if got := opts.Metadata[coreexecutor.RequestedModelMetadataKey]; got != sourceModel {
		t.Fatalf("requested model metadata = %#v, want %q", got, sourceModel)
	}
}
