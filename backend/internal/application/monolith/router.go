package monolith

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
var assets embed.FS

func publicRouter(admin, user http.Handler, roles map[string]http.Handler, ready, syncReady *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/admin/", admin)
	mux.Handle("GET /api/docs/admin.openapi.json", admin)
	mux.Handle("/api/v1/user/", user)
	mux.Handle("/api/v1/public/", user)
	mux.Handle("/metrics", admin)
	checks := make([]http.Handler, 0, len(roles))
	for name, role := range roles {
		checks = append(checks, role)
		mux.HandleFunc("GET /metrics/"+name, func(w http.ResponseWriter, r *http.Request) {
			request := r.Clone(r.Context())
			request.URL = &url.URL{Path: "/metrics"}
			role.ServeHTTP(w, request)
		})
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() || !syncReady.Load() || !allReady(r.Context(), checks) || !ready.Load() || !syncReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("degraded"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	for _, path := range []string{"admin", "h5"} {
		if subtree, err := fs.Sub(assets, "web/"+path); err == nil {
			if index, err := fs.ReadFile(subtree, "index.html"); err == nil {
				prefix := "/" + path + "/"
				mux.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
					name := strings.TrimPrefix(r.URL.Path, prefix)
					if name != "" && fs.ValidPath(name) {
						if file, err := fs.Stat(subtree, name); err == nil && !file.IsDir() {
							http.ServeFileFS(w, r, subtree, name)
							return
						}
					}
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					_, _ = w.Write(index)
				})
			}
		}
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})
	return mux
}

func allReady(ctx context.Context, checks []http.Handler) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ready := make(chan bool, len(checks))
	var group sync.WaitGroup
	for _, handler := range checks {
		group.Go(func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/health/ready", nil)
			if err != nil {
				ready <- false
				return
			}
			out := &localResponse{header: make(http.Header)}
			handler.ServeHTTP(out, req)
			ready <- out.status == http.StatusOK
		})
	}
	for range checks {
		select {
		case up := <-ready:
			if !up {
				cancel()
				group.Wait()
				return false
			}
		case <-ctx.Done():
			group.Wait()
			return false
		}
	}
	group.Wait()
	return true
}
