// The Assembly Line: a product app that Jenkins tests and deploys to
// Clever Cloud, with a factory floor page that shows the line at work.
package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

//go:embed web Jenkinsfile
var files embed.FS

type server struct {
	cfg      Config
	platform Platform
	line     *Line
	page     *template.Template
}

func main() {
	cfg := loadConfig()
	s := newServer(cfg, loadPlatform())

	port := envOr("PORT", "8080")
	httpServer := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
	}
	go func() {
		log.Printf("factory floor on :%s (jenkins=%t, token=%t, job=%s)", port, cfg.JenkinsURL != "", cfg.hasToken(), cfg.Job)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

func newServer(cfg Config, p Platform) *server {
	jenkinsfile, _ := files.ReadFile("Jenkinsfile")
	page := template.Must(template.New("index.html").Funcs(template.FuncMap{
		"jenkinsfile": func() string { return string(jenkinsfile) },
		"inc":         func(i int) int { return i + 1 },
		"asset":       assetURL,
		// svg inlines a brand-kit SVG (trusted file shipped with the app).
		"svg": func(name string) template.HTML {
			b, _ := files.ReadFile("web/" + name)
			return template.HTML(b)
		},
	}).ParseFS(files, "web/index.html"))
	return &server{cfg: cfg, platform: p, line: newLine(cfg), page: page}
}

func (s *server) routes() http.Handler {
	static, _ := fs.Sub(files, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/floor", s.floor)
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /health/deep", s.healthDeep)
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(time.Hour, http.FileServerFS(static))))
	return secure(mux)
}

func (s *server) view(r *http.Request) FloorView {
	return buildFloor(s.cfg, s.platform, s.line.Snapshot(r.Context()))
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := s.page.Execute(w, map[string]any{
		"View":     s.view(r),
		"Platform": s.platform,
	})
	if err != nil {
		log.Printf("render: %v", err)
	}
}

func (s *server) floor(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.view(r))
}

// /health never touches Jenkins: it only says the product is up.
func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// /health/deep reads Jenkins with the API token, through the same code as the page.
func (s *server) healthDeep(w http.ResponseWriter, r *http.Request) {
	snap := s.line.Snapshot(r.Context())
	body := map[string]any{"jenkins": snap.State, "detail": snap.Detail, "job": s.cfg.Job}
	if snap.State == "connected" || snap.State == "no-job" {
		body["builds"] = len(snap.Builds)
		writeJSON(w, http.StatusOK, body)
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, body)
}

// assetURL adds a content hash so a new deployment never serves a stale cached file.
func assetURL(name string) string {
	b, _ := files.ReadFile("web/" + name)
	sum := sha256.Sum256(b)
	return "/static/" + name + "?v=" + hex.EncodeToString(sum[:4])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cacheFor(d time.Duration, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(d.Seconds())))
		h.ServeHTTP(w, r)
	})
}

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; "+
			"style-src 'self' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; "+
			"img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}
