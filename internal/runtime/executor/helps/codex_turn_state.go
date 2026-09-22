package helps

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
	"github.com/tidwall/gjson"
)

func CodexTurnStateEventHeaders(payload []byte) http.Header {
	if gjson.GetBytes(payload, "type").String() != "codex.response.metadata" {
		return nil
	}
	headers := make(http.Header)
	gjson.GetBytes(payload, "headers").ForEach(func(name, value gjson.Result) bool {
		if strings.EqualFold(name.String(), turnstate.Header) {
			if value.Type == gjson.String {
				headers.Add(turnstate.Header, value.String())
			}
			if value.IsArray() {
				for _, item := range value.Array() {
					if item.Type == gjson.String {
						headers.Add(turnstate.Header, item.String())
					}
				}
			}
		}
		return true
	})
	return headers
}
