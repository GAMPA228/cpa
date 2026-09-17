package diagnostics

import (
	"context"
	"io"
	"net/http"
)

// WrapHTTP captures application-level response bytes before executor transforms.
// Transport decompression, TLS and proxy settings are not changed.
func WrapHTTP(ctx context.Context, client *http.Client) {
	if from(ctx) == nil || client == nil {
		return
	}
	if _, ok := client.Transport.(*transport); ok {
		return
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &transport{base: base, ctx: ctx}
}

type transport struct {
	base http.RoundTripper
	ctx  context.Context
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	a := Current(t.ctx)
	if a == nil {
		return t.base.RoundTrip(req)
	}
	a.manager.mu.Lock()
	endpoint := *req.URL
	endpoint.User = nil
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	matches := !a.finished && a.data.Protocol == "http" && endpoint.String() == a.data.URL && a.data.URL != ""
	a.manager.mu.Unlock()
	if !matches {
		return t.base.RoundTrip(req)
	}
	// Tee the actual transmitted body, without consuming it ahead of the transport.
	if req.Body != nil {
		a.manager.mu.Lock()
		size := len(a.data.RequestBody)
		a.bytes -= size
		a.manager.memory -= size
		a.data.RequestBody = nil
		a.manager.mu.Unlock()
		cloned := req.Clone(req.Context())
		cloned.Body = &requestReader{ReadCloser: req.Body, attempt: a}
		req = cloned
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		a.Finish("transport_error", true)
		return resp, err
	}
	if resp == nil {
		a.Finish("empty_response", true)
		return resp, err
	}
	a.Headers(resp.StatusCode, resp.Header, false)
	a.manager.mu.Lock()
	if !a.finished {
		a.data.TransportDecompressed = resp.Uncompressed
	}
	a.manager.mu.Unlock()
	if resp.Body == nil {
		a.Finish("", false)
	} else {
		resp.Body = &bodyReader{ReadCloser: resp.Body, attempt: a, response: resp}
	}
	return resp, err
}

type requestReader struct {
	io.ReadCloser
	attempt *Attempt
}

func (r *requestReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.attempt.manager.mu.Lock()
	r.attempt.appendLocked(p[:n], true)
	r.attempt.manager.mu.Unlock()
	return n, err
}

type bodyReader struct {
	io.ReadCloser
	attempt  *Attempt
	response *http.Response
}

func (r *bodyReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.attempt.Append(p[:n])
	}
	if err == io.EOF {
		r.finish("", false)
	} else if err != nil {
		r.attempt.Finish("read_error", true)
	}
	return n, err
}
func (r *bodyReader) finish(reason string, truncated bool) {
	a := r.attempt
	a.manager.mu.Lock()
	if !a.finished && len(r.response.Trailer) > 0 {
		size := 0
		for key, values := range r.response.Trailer {
			for _, value := range values {
				size += len(key) + len(value) + 32
			}
		}
		if size <= 256<<10 && size <= memoryLimit-a.manager.memory {
			a.data.ResponseTrailers = r.response.Trailer.Clone()
			a.bytes += size
			a.manager.memory += size
		} else {
			a.data.Truncated = true
			a.data.Reason = "trailer_size_limit"
		}
	}
	a.manager.mu.Unlock()
	a.Finish(reason, truncated)
}
func (r *bodyReader) Close() error {
	err := r.ReadCloser.Close()
	// Finish is idempotent. EOF has already captured trailers and completed it.
	r.attempt.Finish("body_closed_before_eof", true)
	return err
}
