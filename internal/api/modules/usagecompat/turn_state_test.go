package usagecompat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/turnstate"
)

func TestTurnStateSettingsRoutesRetired(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "missing-store"
		if existing {
			name = "existing-store"
		}
		t.Run(name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "usage.sqlite3")
			t.Setenv(usageSQLitePathEnv, base)
			path := base + ".turn-state.sqlite3"
			var before []byte
			if existing {
				stored := turnstate.NewManager()
				if err := stored.Open(path); err != nil {
					t.Fatal(err)
				}
				if err := stored.Configure(turnstate.Settings{Enabled: true, MaxChars: 312}); err != nil {
					stored.Close()
					t.Fatal(err)
				}
				stored.Close()
				var err error
				before, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			old := turnstate.Default
			turnstate.Default = turnstate.NewManager()
			t.Cleanup(func() { turnstate.Default.Close(); turnstate.Default = old })
			initial := turnstate.Default.Status()
			router := gin.New()
			module := New(NewHandler(nil, nil), WithMiddleware(func(c *gin.Context) {
				if c.GetHeader("Authorization") != "Bearer test-admin" {
					c.AbortWithStatus(http.StatusUnauthorized)
				}
			}))
			if err := module.Register(router); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				for _, authorized := range []bool{false, true} {
					for _, body := range []string{`{"enabled":false,"max_chars":292}`, "invalid JSON"} {
						req := httptest.NewRequest(method, "/v0/management/usage/turn-state-auto-rules", strings.NewReader(body))
						if authorized {
							req.Header.Set("Authorization", "Bearer test-admin")
						}
						w := httptest.NewRecorder()
						router.ServeHTTP(w, req)
						if !authorized {
							if w.Code != http.StatusUnauthorized {
								t.Fatalf("unauthorized status: %d", w.Code)
							}
							continue
						}
						var out struct {
							Error       string
							Replacement string
						}
						if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
							t.Fatal(err)
						}
						if w.Code != http.StatusGone || out.Error == "" || out.Replacement != turnStatePluginEndpoint || w.Header().Get("Cache-Control") != "no-store" {
							t.Fatalf("unexpected retirement response: %d %s", w.Code, w.Body.String())
						}
					}
				}
			}
			if !reflect.DeepEqual(initial, turnstate.Default.Status()) {
				t.Fatal("registration or legacy request restored or mutated native settings")
			}
			after, err := os.ReadFile(path)
			if existing {
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("legacy store changed")
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("legacy store created: %v", err)
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Fatalf("legacy store sidecar created: %s (%v)", suffix, err)
				}
			}
		})
	}
}
