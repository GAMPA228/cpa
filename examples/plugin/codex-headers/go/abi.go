package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

var active = newHeaderPlugin()

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}
type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func handleMethod(p *headerPlugin, method string, raw []byte) ([]byte, error) {
	var value any
	var err error
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		err = p.configure(raw)
		value = registration()
	case pluginabi.MethodCodexHeadersPrepare:
		value, err = p.prepare(raw)
	case pluginabi.MethodCodexHeadersObserve:
		err = p.observe(raw)
		value = struct{}{}
	case pluginabi.MethodCodexHeadersComplete:
		err = p.complete(raw)
		value = struct{}{}
	case pluginabi.MethodManagementRegister:
		value = managementRegistration()
	case pluginabi.MethodManagementHandle:
		value, err = p.management(raw)
	default:
		return errorEnvelope("unknown_method", "unknown method"), nil
	}
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string) []byte {
	result, _ := json.Marshal(envelope{Error: &envelopeError{Code: code, Message: message}})
	return result
}
