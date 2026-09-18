package usagecompat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
)

func TestTurnStateSettingsRoutesAndRestore(t *testing.T) {
	t.Setenv(usageSQLitePathEnv, filepath.Join(t.TempDir(), "usage.sqlite3"))
	old := turnstate.Default
	turnstate.Default = turnstate.NewManager()
	t.Cleanup(func() { turnstate.Default.Close(); turnstate.Default = old })
	router := gin.New()
	module := New(NewHandler(nil, nil), WithMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer test-admin" {
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	}))
	if err := module.Register(router); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v0/management/usage/turn-state-auto-rules", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if authorized {
			r.Header.Set("Authorization", "Bearer test-admin")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{"GET", "PUT"} {
		if w := call(method, `{"enabled":true,"max_chars":312}`, false); w.Code != 401 {
			t.Fatal("unauthorized access")
		}
	}
	w := call("GET", "", true)
	var status turnstate.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Enabled || status.MaxChars != 292 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect default settings")
	}
	for _, body := range []string{`{}`, `{"enabled":true}`, `{"enabled":true,"max_chars":0}`, `{"enabled":true,"max_chars":8193}`, `{"enabled":true,"max_chars":2.5}`, `{"enabled":true,"max_chars":"312"}`, `{"enabled":true,"max_chars":312,"unknown":1}`} {
		if w := call("PUT", body, true); w.Code != 400 {
			t.Fatalf("invalid input accepted: %s", body)
		}
	}
	if turnstate.Default.Status().Enabled {
		t.Fatal("invalid request changed settings")
	}
	if w := call("PUT", `{"enabled":true,"max_chars":312}`, true); w.Code != 200 {
		t.Fatalf("configure failed: %s", w.Body.String())
	}
	turnstate.Default.Close()
	turnstate.Default = turnstate.NewManager()
	restoreTurnStateStore()
	if status := turnstate.Default.Status(); !status.Enabled || status.MaxChars != 312 {
		t.Fatal("startup failed to restore settings without admin request")
	}
	if w := call("PUT", `{"enabled":false,"max_chars":292}`, true); w.Code != 200 || turnstate.Default.Status().Enabled {
		t.Fatal("disable failed")
	}
}
