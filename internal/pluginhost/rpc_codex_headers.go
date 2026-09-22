package pluginhost

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (a *rpcPluginAdapter) PrepareCodexHeaders(ctx context.Context, req pluginapi.CodexHeaderRequest) (pluginapi.CodexHeaderResponse, error) {
	return callPlugin[pluginapi.CodexHeaderResponse](ctx, a.client, pluginabi.MethodCodexHeadersPrepare, req)
}

func (a *rpcPluginAdapter) ObserveCodexHeaders(ctx context.Context, req pluginapi.CodexHeaderObservation) error {
	_, err := callPlugin[rpcEmptyResponse](ctx, a.client, pluginabi.MethodCodexHeadersObserve, req)
	return err
}

func (a *rpcPluginAdapter) CompleteCodexHeaders(ctx context.Context, req pluginapi.CodexHeaderCompletion) error {
	_, err := callPlugin[rpcEmptyResponse](ctx, a.client, pluginabi.MethodCodexHeadersComplete, req)
	return err
}
