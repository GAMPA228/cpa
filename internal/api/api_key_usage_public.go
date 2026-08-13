package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/modules/apikeyquota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	apiKeyUsagePagePath   = "/api-key-usage.html"
	publicAPIKeyUsagePath = "/v0/api-key-usage"
)

type publicAPIKeyUsageLookupRequest struct {
	APIKey string `json:"api-key"`
}

type publicAPIKeyUsageStatus struct {
	APIKey          string                   `json:"api-key"`
	Remark          string                   `json:"remark,omitempty"`
	DailyTokenLimit int64                    `json:"daily-token-limit"`
	UsedTokens      int64                    `json:"used-tokens"`
	RemainingTokens int64                    `json:"remaining-tokens"`
	RequestCount    int64                    `json:"request-count"`
	Day             string                   `json:"day"`
	ResetAt         time.Time                `json:"reset-at"`
	Limited         bool                     `json:"limited"`
	Exceeded        bool                     `json:"exceeded"`
	UsagePercentage float64                  `json:"usage-percentage"`
	History         []apikeyquota.DailyUsage `json:"history"`
}

func (s *Server) serveAPIKeyUsagePage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(apiKeyUsagePageHTML))
}

func (s *Server) lookupPublicAPIKeyUsage(c *gin.Context) {
	var body publicAPIKeyUsageLookupRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	apiKey := strings.TrimSpace(body.APIKey)
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api-key is required"})
		return
	}
	if s == nil || s.apiKeyQuotaManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "api key usage unavailable"})
		return
	}

	now := time.Now()
	history, found, err := s.apiKeyQuotaManager.History(apiKey, now, 7)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "api key usage unavailable"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "api key not found"})
		return
	}
	status, found, err := s.apiKeyQuotaManager.Lookup(apiKey, now)
	if err != nil || !found {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "api key usage unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": publicAPIKeyUsageStatusFrom(status, history)})
}

func publicAPIKeyUsageStatusFrom(status apikeyquota.Status, history []apikeyquota.DailyUsage) publicAPIKeyUsageStatus {
	usagePercentage := float64(0)
	if status.Limited && status.DailyTokenLimit > 0 && status.UsedTokens > 0 {
		usagePercentage = float64(status.UsedTokens) * 100 / float64(status.DailyTokenLimit)
	}
	return publicAPIKeyUsageStatus{
		APIKey:          util.HideAPIKey(status.APIKey),
		Remark:          status.Remark,
		DailyTokenLimit: status.DailyTokenLimit,
		UsedTokens:      status.UsedTokens,
		RemainingTokens: status.RemainingTokens,
		RequestCount:    status.RequestCount,
		Day:             status.Day,
		ResetAt:         status.ResetAt,
		Limited:         status.Limited,
		Exceeded:        status.Exceeded,
		UsagePercentage: usagePercentage,
		History:         history,
	}
}

const apiKeyUsagePageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>API Key 用量查询</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #e9f0ec;
      --panel: #ffffff;
      --panel-soft: #f3f7f4;
      --text: #202225;
      --muted: #697077;
      --border: #dfe4dd;
      --primary: #256f5a;
      --primary-dark: #184c3d;
      --danger: #b42318;
      --success: #16704b;
      --shadow: 0 18px 36px rgba(31, 41, 55, 0.08);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft YaHei", sans-serif;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 32px 0;
      background:
        linear-gradient(135deg, #eef4ef 0%, #dfeaf0 48%, #f3efe4 100%);
      color: var(--text);
    }
    .shell {
      width: min(960px, calc(100% - 32px));
      margin: 0 auto;
    }
    header {
      margin-bottom: 22px;
    }
    h1 {
      margin: 0;
      font-size: 30px;
      line-height: 1.2;
      letter-spacing: 0;
    }
    .panel {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 8px;
      box-shadow: var(--shadow);
    }
    form {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 12px;
      padding: 18px;
      align-items: end;
    }
    label {
      display: block;
      margin-bottom: 8px;
      color: var(--muted);
      font-size: 13px;
      font-weight: 700;
    }
    input {
      width: 100%;
      height: 42px;
      border: 1px solid var(--border);
      border-radius: 6px;
      padding: 0 12px;
      color: var(--text);
      background: #fff;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 14px;
      outline: none;
    }
    input:focus {
      border-color: var(--primary);
      box-shadow: 0 0 0 3px rgba(37, 111, 90, 0.14);
    }
    button {
      height: 42px;
      min-width: 108px;
      border: 0;
      border-radius: 6px;
      padding: 0 18px;
      color: #fff;
      background: var(--primary);
      font-size: 14px;
      font-weight: 800;
      cursor: pointer;
    }
    button:hover { background: var(--primary-dark); }
    button:disabled {
      cursor: not-allowed;
      opacity: 0.65;
    }
    .message {
      display: none;
      margin-top: 14px;
      padding: 12px 14px;
      border-radius: 6px;
      font-size: 14px;
      border: 1px solid var(--border);
      background: var(--panel);
    }
    .message.error {
      display: block;
      color: var(--danger);
      border-color: rgba(180, 35, 24, 0.32);
      background: rgba(180, 35, 24, 0.07);
    }
    .message.info {
      display: block;
      color: var(--primary-dark);
      border-color: rgba(37, 111, 90, 0.28);
      background: rgba(37, 111, 90, 0.08);
    }
    .result {
      display: none;
      margin-top: 18px;
      overflow: hidden;
      border-radius: 4px;
      box-shadow: 0 12px 28px rgba(31, 41, 55, 0.08);
    }
    .result.show { display: block; }
    .result-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 14px;
      padding: 16px 18px;
      border-bottom: 1px solid var(--border);
      background: var(--panel-soft);
    }
    .key {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 15px;
      font-weight: 800;
      word-break: break-all;
    }
    .status {
      white-space: nowrap;
      border-radius: 999px;
      padding: 6px 10px;
      font-size: 12px;
      font-weight: 800;
      color: var(--success);
      background: rgba(22, 112, 75, 0.1);
      border: 1px solid rgba(22, 112, 75, 0.28);
    }
    .status.danger {
      color: var(--danger);
      background: rgba(180, 35, 24, 0.08);
      border-color: rgba(180, 35, 24, 0.28);
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      background: var(--panel);
    }
    .item {
      min-width: 0;
      padding: 16px 18px;
      background: var(--panel);
      border-right: 1px solid var(--border);
      border-bottom: 1px solid var(--border);
    }
    .item:nth-child(4n) {
      border-right: 0;
    }
    .item:nth-last-child(-n + 4) {
      border-bottom: 0;
    }
    .item span {
      display: block;
      color: var(--muted);
      font-size: 13px;
      font-weight: 700;
      margin-bottom: 8px;
    }
    .item strong {
      display: block;
      color: var(--text);
      font-size: 20px;
      line-height: 1.25;
      word-break: break-word;
    }
    .share-meter {
      height: 6px;
      margin-top: 10px;
      overflow: hidden;
      border-radius: 999px;
      background: #e5ebe7;
    }
    .share-meter > span {
      display: block;
      width: 0;
      height: 100%;
      border-radius: inherit;
      background: var(--primary);
      transition: width 240ms ease;
    }
    .history {
      padding: 18px;
      border-top: 1px solid var(--border);
      background: var(--panel);
    }
    .history-head {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: 12px;
      margin-bottom: 12px;
    }
    .history-head h2 {
      margin: 0;
      font-size: 17px;
    }
    .history-head span {
      color: var(--muted);
      font-size: 12px;
    }
    .history-list {
      overflow: hidden;
      border: 1px solid var(--border);
      border-radius: 6px;
    }
    .history-row {
      display: grid;
      grid-template-columns: 105px minmax(120px, 1fr) 105px 90px;
      align-items: center;
      gap: 14px;
      min-height: 42px;
      padding: 9px 12px;
      border-bottom: 1px solid var(--border);
      font-size: 13px;
    }
    .history-row:last-child { border-bottom: 0; }
    .history-row.today { background: var(--panel-soft); }
    .history-date { font-weight: 700; }
    .history-value, .history-requests { text-align: right; }
    .history-value { font-weight: 800; }
    .history-requests { color: var(--muted); }
    .history-bar {
      height: 7px;
      overflow: hidden;
      border-radius: 999px;
      background: #e5ebe7;
    }
    .history-bar > span {
      display: block;
      height: 100%;
      min-width: 0;
      border-radius: inherit;
      background: var(--primary);
    }
    @media (max-width: 720px) {
      body {
        align-items: flex-start;
      }
      .shell {
        width: min(100% - 24px, 960px);
      }
      form {
        grid-template-columns: 1fr;
      }
      button {
        width: 100%;
      }
      .result-head {
        display: block;
      }
      .status {
        display: inline-flex;
        margin-top: 10px;
      }
      .grid {
        grid-template-columns: 1fr;
      }
      .item {
        border-right: 0;
      }
      .item:nth-last-child(-n + 4) {
        border-bottom: 1px solid var(--border);
      }
      .item:last-child {
        border-bottom: 0;
      }
      .history-row {
        grid-template-columns: 84px minmax(60px, 1fr) 82px;
        gap: 8px;
      }
      .history-requests { display: none; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <header>
      <h1>API Key 用量查询</h1>
    </header>

    <section class="panel">
      <form id="lookup-form">
        <div>
          <label for="api-key">API Key</label>
          <input id="api-key" name="api-key" type="password" autocomplete="off" spellcheck="false" placeholder="sk-..." required>
        </div>
        <button id="submit-button" type="submit">查询</button>
      </form>
    </section>

    <div id="message" class="message" role="status"></div>

    <section id="result" class="panel result" aria-live="polite">
      <div class="result-head">
        <div>
          <div id="result-key" class="key">-</div>
        </div>
        <span id="result-status" class="status">-</span>
      </div>
      <div class="grid">
        <div class="item"><span>今日已用</span><strong id="used-tokens">-</strong></div>
        <div class="item">
          <span>今日用量占比</span>
          <strong id="usage-percentage">-</strong>
          <div class="share-meter" aria-hidden="true"><span id="usage-percentage-bar"></span></div>
        </div>
        <div class="item"><span>剩余额度</span><strong id="remaining-tokens">-</strong></div>
        <div class="item"><span>每日额度</span><strong id="daily-limit">-</strong></div>
        <div class="item"><span>近 7 天已用</span><strong id="seven-day-tokens">-</strong></div>
        <div class="item"><span>今日请求次数</span><strong id="request-count">-</strong></div>
        <div class="item"><span>统计日期</span><strong id="usage-day">-</strong></div>
        <div class="item"><span>重置时间</span><strong id="reset-at">-</strong></div>
      </div>
      <div class="history">
        <div class="history-head">
          <h2>近 7 天用量</h2>
          <span>按自然日统计，包含今天</span>
        </div>
        <div id="history-list" class="history-list"></div>
      </div>
    </section>
  </main>

  <script>
    const form = document.getElementById('lookup-form');
    const input = document.getElementById('api-key');
    const button = document.getElementById('submit-button');
    const message = document.getElementById('message');
    const result = document.getElementById('result');

    function setMessage(text, type) {
      message.textContent = text || '';
      message.className = text ? 'message ' + type : 'message';
    }

    function formatTokens(value) {
      const normalized = Number.isFinite(Number(value)) ? Math.max(0, Number(value)) : 0;
      const millions = normalized / 1000000;
      return millions.toLocaleString('zh-CN', {
        minimumFractionDigits: 1,
        maximumFractionDigits: 1
      }) + 'M';
    }

    function formatDateTime(value) {
      if (!value) return '-';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return '-';
      return date.toLocaleString('zh-CN', { hour12: false });
    }

    function renderHistory(history, currentDay) {
      const entries = Array.isArray(history) ? history : [];
      const list = document.getElementById('history-list');
      list.replaceChildren();
      const maxTokens = entries.reduce((max, entry) => Math.max(max, Number(entry['used-tokens'] || 0)), 0);
      let sevenDayTokens = 0;

      entries.forEach((entry) => {
        const usedTokens = Math.max(0, Number(entry['used-tokens'] || 0));
        const requestCount = Math.max(0, Number(entry['request-count'] || 0));
        sevenDayTokens += usedTokens;

        const row = document.createElement('div');
        row.className = 'history-row' + (entry.day === currentDay ? ' today' : '');

        const date = document.createElement('span');
        date.className = 'history-date';
        date.textContent = entry.day || '-';

        const bar = document.createElement('div');
        bar.className = 'history-bar';
        const barFill = document.createElement('span');
        const width = maxTokens > 0 ? usedTokens / maxTokens * 100 : 0;
        barFill.style.width = (usedTokens > 0 ? Math.max(2, width) : 0) + '%';
        bar.appendChild(barFill);

        const value = document.createElement('span');
        value.className = 'history-value';
        value.textContent = formatTokens(usedTokens);

        const requests = document.createElement('span');
        requests.className = 'history-requests';
        requests.textContent = requestCount.toLocaleString('zh-CN') + ' 次';

        row.append(date, bar, value, requests);
        list.appendChild(row);
      });

      if (entries.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'history-row';
        empty.textContent = '暂无近 7 天数据';
        list.appendChild(empty);
      }
      return sevenDayTokens;
    }

    function renderStatus(item) {
      document.getElementById('result-key').textContent = item['api-key'] || '-';
      document.getElementById('used-tokens').textContent = formatTokens(item['used-tokens']);
      document.getElementById('remaining-tokens').textContent = item.limited ? formatTokens(item['remaining-tokens']) : '不限';
      document.getElementById('daily-limit').textContent = item.limited ? formatTokens(item['daily-token-limit']) : '不限';
      document.getElementById('request-count').textContent = Number(item['request-count'] || 0).toLocaleString('zh-CN');
      document.getElementById('usage-day').textContent = item.day || '-';
      document.getElementById('reset-at').textContent = formatDateTime(item['reset-at']);

      const usagePercentage = Math.max(0, Number(item['usage-percentage'] || 0));
      document.getElementById('usage-percentage').textContent = item.limited ? usagePercentage.toFixed(1) + '%' : '不限';
      document.getElementById('usage-percentage-bar').style.width = item.limited ? Math.min(100, usagePercentage) + '%' : '0';
      const sevenDayTokens = renderHistory(item.history, item.day);
      document.getElementById('seven-day-tokens').textContent = formatTokens(sevenDayTokens);

      const status = document.getElementById('result-status');
      status.textContent = item.exceeded ? '已超额' : (item.limited ? '可用' : '未限额');
      status.className = item.exceeded ? 'status danger' : 'status';
      result.classList.add('show');
    }

    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      const apiKey = input.value.trim();
      if (!apiKey) {
        setMessage('请输入 API Key。', 'error');
        return;
      }

      button.disabled = true;
      button.textContent = '查询中';
      result.classList.remove('show');
      setMessage('', '');

      try {
        const response = await fetch('/v0/api-key-usage', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ 'api-key': apiKey })
        });
        let payload = {};
        try {
          payload = await response.json();
        } catch (_) {
          payload = {};
        }

        if (response.status === 404) {
          setMessage('没有找到这个 API Key。', 'error');
          return;
        }
        if (!response.ok) {
          setMessage(payload.error || '查询失败，请稍后重试。', 'error');
          return;
        }
        if (!payload.item) {
          setMessage('查询结果为空。', 'error');
          return;
        }
        renderStatus(payload.item);
        setMessage('查询成功。', 'info');
      } catch (error) {
        setMessage('查询失败，请检查服务是否可访问。', 'error');
      } finally {
        button.disabled = false;
        button.textContent = '查询';
      }
    });
  </script>
</body>
</html>`
