package diagnostics

import (
	"net/http"
	"strings"
)

// RequestHeaderSnapshot contains application-level headers, not a wire transcript.
// It is bounded and redacted so WebSocket sessions can retain it for later captures.
type RequestHeaderSnapshot struct {
	Headers   http.Header
	Truncated bool
}

func credentialHeader(name string) bool {
	name = strings.ToLower(name)
	name = strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(name)
	return strings.Contains(name, "authorization") || strings.Contains(name, "cookie") ||
		strings.Contains(name, "apikey") || strings.Contains(name, "token") ||
		strings.Contains(name, "secret") || strings.Contains(name, "password") ||
		strings.Contains(name, "credential") || strings.Contains(name, "proxyauth") ||
		strings.Contains(name, "authentication") || name == "xauth" || name == "auth"
}

func requestHeaderSize(headers http.Header) int {
	size := 0
	for key, values := range headers {
		size += len(key) + 64
		for _, value := range values {
			size += len(value) + 32
		}
	}
	return size
}

func SnapshotRequestHeaders(req *http.Request) RequestHeaderSnapshot {
	if req == nil {
		return RequestHeaderSnapshot{}
	}
	headers := make(http.Header)
	size := 0
	for key, values := range req.Header {
		// Host is carried separately by net/http and overrides any map entry.
		if strings.EqualFold(key, "Host") {
			continue
		}
		key = http.CanonicalHeaderKey(key)
		size += len(key) + 64
		redact := credentialHeader(key)
		if redact {
			size += len("[REDACTED]") + 32
		} else {
			for _, value := range values {
				size += len(value) + 32
			}
		}
		if size > 256<<10 {
			return RequestHeaderSnapshot{Truncated: true}
		}
		if redact {
			headers[key] = []string{"[REDACTED]"}
		} else {
			headers[key] = append(headers[key], values...)
		}
	}
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	if host != "" {
		headers.Set("Host", host)
	}
	if requestHeaderSize(headers) > 256<<10 {
		return RequestHeaderSnapshot{Truncated: true}
	}
	return RequestHeaderSnapshot{Headers: headers}
}

func (a *Attempt) RequestHeaders(snapshot RequestHeaderSnapshot) {
	if a == nil {
		return
	}
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.finished || a.data.RequestHeaders != nil {
		return
	}
	size := requestHeaderSize(snapshot.Headers)
	if snapshot.Truncated || size > 256<<10 || size > memoryLimit-m.memory {
		a.data.Truncated = true
		a.data.Reason = "request_header_size_limit"
		return
	}
	a.data.RequestHeaders = snapshot.Headers.Clone()
	a.bytes += size
	m.memory += size
}
