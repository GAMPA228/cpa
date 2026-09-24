package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func externalUpdateHandler(t *testing.T, badChecksum bool) (*Handler, string) {
	t.Helper()
	id := "sample-provider"
	pluginsDir := writeManagementPluginFile(t, id)
	host := pluginhost.New()
	host.RegisterPluginForTest(id, pluginapi.Plugin{Metadata: pluginapi.Metadata{
		Name: "Sample Provider", Version: "0.1.0", GitHubRepository: "https://github.com/example/sample-provider",
	}})
	extension := managementPluginExtension(runtime.GOOS)
	assetName := fmt.Sprintf("%s-%s-%s%s", id, runtime.GOOS, runtime.GOARCH, extension)
	assetURL := "https://example.test/" + assetName
	checksumURL := "https://example.test/" + assetName + ".sha256"
	data := []byte("new plugin library")
	hash := sha256.Sum256(data)
	checksum := hex.EncodeToString(hash[:])
	if badChecksum {
		checksum = strings.Repeat("0", 64)
	}
	release, errJSON := json.Marshal(map[string]any{
		"tag_name": "v0.2.0",
		"assets": []map[string]string{
			{"name": assetName, "browser_download_url": assetURL},
			{"name": assetName + ".sha256", "browser_download_url": checksumURL},
		},
	})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{id: pluginConfigFromYAML(t, "enabled: true\nmode: preserved\n")}}},
		pluginHost: host,
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://api.github.com/repos/example/sample-provider/releases/latest": release,
			assetURL:    data,
			checksumURL: []byte(checksum + "  " + assetName + "\n"),
		},
	}
	return h, pluginsDir
}

func externalUpdateRequest(h *Handler, method string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(method, "/v0/management/plugins/sample-provider/external-update", nil)
	if method == http.MethodGet {
		h.CheckExternalPluginUpdate(c)
	} else {
		h.UpdateExternalPlugin(c)
	}
	return rec
}

func TestExternalPluginUpdateStagesVerifiedLibrary(t *testing.T) {
	h, pluginsDir := externalUpdateHandler(t, false)
	check := externalUpdateRequest(h, http.MethodGet)
	if check.Code != http.StatusOK {
		t.Fatalf("check status = %d; body = %s", check.Code, check.Body.String())
	}
	var info externalPluginUpdateInfo
	if errJSON := json.Unmarshal(check.Body.Bytes(), &info); errJSON != nil {
		t.Fatal(errJSON)
	}
	if !info.Supported || !info.UpdateAvailable || info.LatestVersion != "0.2.0" {
		t.Fatalf("update info = %+v", info)
	}
	update := externalUpdateRequest(h, http.MethodPost)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"restart_required":true`) {
		t.Fatalf("update status = %d; body = %s", update.Code, update.Body.String())
	}
	oldFile := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider"+managementPluginExtension(runtime.GOOS))
	newFile := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.2.0"+managementPluginExtension(runtime.GOOS))
	oldData, errOld := os.ReadFile(oldFile)
	newData, errNew := os.ReadFile(newFile)
	if errOld != nil || errNew != nil || string(oldData) != "x" || string(newData) != "new plugin library" {
		t.Fatalf("old=%q (%v), new=%q (%v)", oldData, errOld, newData, errNew)
	}
	if got := pluginRawScalarValue(t, h.cfg.Plugins.Configs["sample-provider"], "mode"); got != "preserved" {
		t.Fatalf("plugin config mode = %q", got)
	}
}

func TestExternalPluginUpdateRejectsBadChecksum(t *testing.T) {
	h, pluginsDir := externalUpdateHandler(t, true)
	update := externalUpdateRequest(h, http.MethodPost)
	if update.Code != http.StatusBadGateway || !strings.Contains(update.Body.String(), "plugin_checksum_failed") {
		t.Fatalf("update status = %d; body = %s", update.Code, update.Body.String())
	}
	newFile := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider-v0.2.0"+managementPluginExtension(runtime.GOOS))
	if _, errStat := os.Stat(newFile); !os.IsNotExist(errStat) {
		t.Fatalf("new plugin file exists after checksum failure: %v", errStat)
	}
}

func TestExternalPluginUpdateRejectsStoreManagedPlugin(t *testing.T) {
	h, _ := externalUpdateHandler(t, false)
	h.cfg.Plugins.Configs["sample-provider"] = pluginConfigFromYAML(t, "enabled: true\nstore:\n  id: sample-provider\n  source-id: official\n")
	check := externalUpdateRequest(h, http.MethodGet)
	if check.Code != http.StatusConflict || !strings.Contains(check.Body.String(), "plugin_store_managed") {
		t.Fatalf("check status = %d; body = %s", check.Code, check.Body.String())
	}
}
