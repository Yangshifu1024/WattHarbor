package monolith

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// localTransport 保留现有内部 handler 的鉴权和契约，在同一进程内调用。
type localTransport map[string]http.Handler

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/internal/") {
		return nil, errors.New("local transport only serves internal routes")
	}
	handler := t[r.URL.Host]
	if handler == nil {
		return nil, fmt.Errorf("unknown local service %q", r.URL.Host)
	}
	req := r.Clone(r.Context())
	req.RemoteAddr = "127.0.0.1:0"
	w := &localResponse{header: make(http.Header)}
	handler.ServeHTTP(w, req)
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if w.overflow {
		return nil, errors.New("local response exceeded 4 MiB")
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: w.header, Body: io.NopCloser(&w.body), ContentLength: int64(w.body.Len()), Request: r,
	}, nil
}

type localResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *localResponse) Header() http.Header { return w.header }
func (w *localResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *localResponse) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if w.overflow || len(p) > (4<<20)-w.body.Len() {
		w.overflow = true
		return 0, errors.New("local response exceeded 4 MiB")
	}
	return w.body.Write(p)
}
