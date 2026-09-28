//go:build ignore

// This test-only HTTP/2 server never contacts an external provider.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	for _, port := range []string{"18319", "18320"} {
		port := port
		go func() {
			_ = http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || (r.Host != "ipwho.is:443" && r.Host != "chatgpt.com:443") {
					http.Error(w, "unexpected target", 400)
					return
				}
				upstream, err := net.Dial("tcp", "127.0.0.1:443")
				if err != nil {
					http.Error(w, "connect", 502)
					return
				}
				client, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					_ = upstream.Close()
					return
				}
				if file, err := os.OpenFile(os.Args[3]+"."+port, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
					_, _ = file.WriteString(r.Host + "\n")
					_ = file.Close()
				}
				_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
				_, _ = io.Copy(client, upstream)
				_ = client.Close()
			}))
		}()
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "ipwho.is" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"ip":"203.0.113.7","timezone":{"id":"America/New_York"}}`))
	})
	http.HandleFunc("/backend-api/codex/responses", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", 400)
			return
		}
		if err := os.WriteFile(os.Args[3], body, 0600); err != nil {
			http.Error(w, "write", 500)
			return
		}
		var input map[string]any
		if json.Unmarshal(body, &input) != nil {
			http.Error(w, "json", 400)
			return
		}
		event := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_test", "object": "response", "model": input["model"], "status": "completed", "output": []any{map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "test", "annotations": []any{}}}}}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}
		payload, _ := json.Marshal(event)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(append(append([]byte("data: "), payload...), '\n', '\n'))
	})
	if err := http.ListenAndServeTLS("127.0.0.1:443", os.Args[1], os.Args[2], nil); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
