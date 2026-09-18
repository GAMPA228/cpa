package helps

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// responseModelFromPayload reads only explicit upstream fields, never request defaults.
func responseModelFromPayload(payload []byte) string {
	payload = jsonPayload(payload)
	if !bytes.Contains(payload, []byte(`"model"`)) || !gjson.ValidBytes(payload) {
		return ""
	}
	for _, path := range []string{"response.model", "model"} {
		value := gjson.GetBytes(payload, path)
		if value.Type == gjson.String {
			if model := strings.TrimSpace(value.String()); model != "" {
				return model
			}
		}
	}
	return ""
}

// ObserveResponseModel must receive raw upstream JSON before client-facing rewrites.
func (r *UsageReporter) ObserveResponseModel(payload []byte) {
	if r == nil {
		return
	}
	if model := responseModelFromPayload(payload); model != "" {
		r.responseModelMu.Lock()
		r.responseModel = model
		r.responseModelMu.Unlock()
	}
}
