package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestExecuteImageWithAuthManagerPreservesMultipartModelRewrite(t *testing.T) {
	for _, field := range []string{"image", "image[]"} {
		for _, apiKey := range []string{"sk-user", "sk-whitelist"} {
			t.Run(field+"/"+apiKey, func(t *testing.T) {
				const sourceModel = "gpt-image-2"
				const targetModel = "gpt-image-2.5-sunburst"
				wantModel := targetModel
				if apiKey == "sk-whitelist" {
					wantModel = sourceModel
				}
				executor := &modelExecutionCaptureExecutor{}
				cfg := modelRewriteTestConfig(sourceModel, targetModel)
				handler := newModelExecutionHandler(t, wantModel, executor, cfg)
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				for key, value := range map[string]string{"model": sourceModel, "prompt": "Edit this image", "size": "1024x1024", "n": "1"} {
					if err := writer.WriteField(key, value); err != nil {
						t.Fatal(err)
					}
				}
				for _, name := range []string{field, field, "mask"} {
					part, err := writer.CreateFormFile(name, "test.png")
					if err != nil {
						t.Fatal(err)
					}
					if _, err = part.Write([]byte("\x89PNG\x00test-image")); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				ctx := modelRewriteContextWithAPIKey(t, apiKey)
				ginCtx := ctx.Value("gin").(*gin.Context)
				ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
				ginCtx.Request.Header.Set("Content-Type", writer.FormDataContentType())
				_, _, errMsg := handler.ExecuteImageWithAuthManager(ctx, "openai-image", sourceModel, body.Bytes(), "")
				if errMsg != nil {
					t.Fatalf("image execution error: %+v", errMsg)
				}
				req, opts := executor.captured()
				if req.Model != wantModel {
					t.Fatalf("model = %q, want %q", req.Model, wantModel)
				}
				if !bytes.Equal(req.Payload, body.Bytes()) || !bytes.Equal(opts.OriginalRequest, body.Bytes()) {
					t.Fatal("multipart upload was modified before reaching the image executor")
				}
				if opts.Headers.Get("Content-Type") != writer.FormDataContentType() {
					t.Fatal("multipart boundary was not preserved")
				}
			})
		}
	}
}

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

func TestExecuteWithAuthManagerCloaksRewrittenModelInResponse(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.6-terra"
	executor := &modelExecutionCaptureExecutor{
		execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{Payload: []byte(`{"model":"gpt-5.6-terra","response":{"model":"gpt-5.6-terra"}}`)}, nil
		},
	}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"input":[]}`, sourceModel))
	resp, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai-response", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if got := gjson.GetBytes(resp, "model").String(); got != sourceModel {
		t.Fatalf("response model = %q, want %q", got, sourceModel)
	}
	if got := gjson.GetBytes(resp, "response.model").String(); got != sourceModel {
		t.Fatalf("nested response model = %q, want %q", got, sourceModel)
	}
	if strings.Contains(string(resp), targetModel) {
		t.Fatalf("response leaked rewritten model: %s", resp)
	}
}

func TestExecuteWithAuthManagerSanitizesRewrittenModelServiceError(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.6-terra"
	executor := &modelExecutionCaptureExecutor{
		execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{}, &coreauth.Error{
				Code:       "upstream_unavailable",
				Message:    "provider=codex model=" + targetModel + " url=https://internal.example/v1/responses",
				HTTPStatus: http.StatusServiceUnavailable,
			}
		},
	}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"input":[]}`, sourceModel))
	_, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai-response", sourceModel, body, "")
	if errMsg == nil || errMsg.Error == nil {
		t.Fatal("ExecuteWithAuthManager() error = nil")
	}
	if errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
	}
	if got := errMsg.Error.Error(); got != modelRewriteServiceUnavailableBody {
		t.Fatalf("public error = %q, want %q", got, modelRewriteServiceUnavailableBody)
	}
	for _, secret := range []string{targetModel, "codex", "internal.example"} {
		if strings.Contains(errMsg.Error.Error(), secret) {
			t.Fatalf("public error leaked %q: %s", secret, errMsg.Error)
		}
	}
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

func TestExecuteWithAuthManagerRewritesModelThinkingEffort(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.5(high)"
	executor := &modelExecutionCaptureExecutor{}
	cfg := modelRewriteTestConfig(sourceModel, "gpt-5.5")
	cfg.ModelRewrite.Rules[0].TargetThinkingEffort = "high"
	handler := newModelExecutionHandler(t, targetModel, executor, cfg)

	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[],"reasoning_effort":"ultra"}`, sourceModel))
	_, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}

	gotReq, gotOpts := executor.captured()
	assertModelRewriteApplied(t, gotReq, gotOpts, sourceModel, targetModel)
	if got := gjson.GetBytes(gotReq.Payload, "reasoning_effort").String(); got != "high" {
		t.Fatalf("payload reasoning_effort = %q, want high", got)
	}
}

func TestExecuteWithAuthManagerRewritesResponseThinkingEffort(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.5(high)"
	executor := &modelExecutionCaptureExecutor{}
	cfg := modelRewriteTestConfig(sourceModel, "gpt-5.5")
	cfg.ModelRewrite.Rules[0].TargetThinkingEffort = "high"
	handler := newModelExecutionHandler(t, targetModel, executor, cfg)

	body := []byte(fmt.Sprintf(`{"model":%q,"input":[],"reasoning":{"effort":"ultra"}}`, sourceModel))
	_, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai-response", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}

	gotReq, _ := executor.captured()
	if gotReq.Model != targetModel {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, targetModel)
	}
	if got := gjson.GetBytes(gotReq.Payload, "reasoning.effort").String(); got != "high" {
		t.Fatalf("payload reasoning.effort = %q, want high", got)
	}
}

func TestExecuteWithAuthManagerRewritesResponseModelPreservesUltraThinkingEffort(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.6-terra"
	executor := &modelExecutionCaptureExecutor{}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"input":[],"reasoning":{"effort":"ultra"}}`, sourceModel))
	_, _, errMsg := handler.ExecuteWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai-response", sourceModel, body, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}

	gotReq, gotOpts := executor.captured()
	assertModelRewriteApplied(t, gotReq, gotOpts, sourceModel, targetModel)
	if got := gjson.GetBytes(gotReq.Payload, "reasoning.effort").String(); got != "ultra" {
		t.Fatalf("payload reasoning.effort = %q, want ultra", got)
	}
	if got := gotOpts.Metadata[coreexecutor.ReasoningEffortMetadataKey]; got != "ultra" {
		t.Fatalf("reasoning effort metadata = %#v, want ultra", got)
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

func TestExecuteStreamWithAuthManagerCloaksRewrittenModelInSSE(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.6-terra"
	executor := &modelExecutionCaptureExecutor{
		stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte(`event: response.completed
data: {"type":"response.completed","response":{"model":"gpt-5.6-terra","output":[]}}

`)}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, sourceModel))
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai-response", sourceModel, body, "")
	var response strings.Builder
	for chunk := range dataChan {
		response.Write(chunk)
	}
	for errMsg := range errChan {
		if errMsg != nil {
			t.Fatalf("ExecuteStreamWithAuthManager() error = %+v", errMsg)
		}
	}
	if !strings.Contains(response.String(), `"model":"`+sourceModel+`"`) {
		t.Fatalf("stream response missing requested model: %s", response.String())
	}
	if strings.Contains(response.String(), targetModel) {
		t.Fatalf("stream response leaked rewritten model: %s", response.String())
	}
}

func TestExecuteStreamWithAuthManagerSanitizesRewrittenModelBootstrapError(t *testing.T) {
	sourceModel := "gpt-5.6-sol"
	targetModel := "gpt-5.6-terra"
	executor := &modelExecutionCaptureExecutor{
		stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			return nil, &coreauth.Error{
				Code:       "auth_unavailable",
				Message:    "no auth available for " + targetModel,
				HTTPStatus: http.StatusServiceUnavailable,
			}
		},
	}
	handler := newModelExecutionHandler(t, targetModel, executor, modelRewriteTestConfig(sourceModel, targetModel))

	body := []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, sourceModel))
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(modelRewriteContextWithAPIKey(t, "sk-user"), "openai-response", sourceModel, body, "")
	if dataChan != nil {
		for range dataChan {
			t.Fatal("unexpected stream payload")
		}
	}
	var gotErr *interfaces.ErrorMessage
	for errMsg := range errChan {
		if errMsg != nil {
			gotErr = errMsg
		}
	}
	if gotErr == nil || gotErr.Error == nil {
		t.Fatal("stream error = nil")
	}
	if gotErr.StatusCode != http.StatusServiceUnavailable || gotErr.Error.Error() != modelRewriteServiceUnavailableBody {
		t.Fatalf("stream error = status %d body %q", gotErr.StatusCode, gotErr.Error.Error())
	}
	if strings.Contains(gotErr.Error.Error(), targetModel) {
		t.Fatalf("stream error leaked rewritten model: %s", gotErr.Error)
	}
}

func TestCloakModelRewriteResponseWebsocketPayload(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"model":"gpt-5.6-terra"}}`)
	got := cloakModelRewriteResponse(payload, "gpt-5.6-sol", "gpt-5.6-terra", true)
	if model := gjson.GetBytes(got, "response.model").String(); model != "gpt-5.6-sol" {
		t.Fatalf("response.model = %q, want gpt-5.6-sol", model)
	}
	if strings.Contains(string(got), "gpt-5.6-terra") {
		t.Fatalf("websocket payload leaked rewritten model: %s", got)
	}
}

func TestCloakModelRewriteResponseWebsocketErrorPayload(t *testing.T) {
	payload := []byte(`{"type":"error","error":{"message":"gpt-5.6-terra unavailable","model":"gpt-5.6-terra","provider":"codex","url":"https://internal.example"}}`)
	got := cloakModelRewriteResponse(payload, "gpt-5.6-sol", "gpt-5.6-terra", true)
	if model := gjson.GetBytes(got, "error.model").String(); model != "gpt-5.6-sol" {
		t.Fatalf("error.model = %q, want gpt-5.6-sol", model)
	}
	for _, secret := range []string{"gpt-5.6-terra", "codex", "internal.example"} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("websocket error payload leaked %q: %s", secret, got)
		}
	}
}

func TestSanitizeModelRewriteClientErrorPreservesDirectPolicyResponse(t *testing.T) {
	original := &interfaces.ErrorMessage{
		StatusCode:     http.StatusTooManyRequests,
		DirectResponse: true,
		Body:           []byte(`{"error":{"message":"出错了，请联系管理员"}}`),
	}
	got := sanitizeModelRewriteErrorMessage(context.Background(), original, "gpt-5.6-sol", "gpt-5.6-terra", true)
	if got != original {
		t.Fatal("direct policy response should remain unchanged")
	}
}

func TestSanitizeModelRewriteClientErrorCloaksRateLimitDetails(t *testing.T) {
	original := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New(`{"error":{"code":"model_cooldown","model":"gpt-5.6-terra","provider":"codex","url":"https://internal.example","message":"gpt-5.6-terra is cooling down"}}`),
	}
	got := sanitizeModelRewriteErrorMessage(context.Background(), original, "gpt-5.6-sol", "gpt-5.6-terra", true)
	if got == nil || got.Error == nil {
		t.Fatal("sanitized error = nil")
	}
	if got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", got.StatusCode, http.StatusTooManyRequests)
	}
	body := got.Error.Error()
	if model := gjson.Get(body, "error.model").String(); model != "gpt-5.6-sol" {
		t.Fatalf("error.model = %q, want gpt-5.6-sol", model)
	}
	for _, secret := range []string{"gpt-5.6-terra", "codex", "internal.example"} {
		if strings.Contains(body, secret) {
			t.Fatalf("sanitized rate-limit error leaked %q: %s", secret, body)
		}
	}
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
