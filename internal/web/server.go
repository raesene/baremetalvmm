package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/raesene/baremetalvmm/internal/config"
	webfs "github.com/raesene/baremetalvmm/web"
)

type Server struct {
	cfg          *config.Config
	password     string
	listenAddr   string
	sessions     *sessionStore
	loginLimiter *rateLimiter
	templates    map[string]*template.Template
	router       chi.Router
	sseBroker    *SSEBroker
	apiKey       string
}

func NewServer(cfg *config.Config, password, listenAddr string) (*Server, error) {
	b := make([]byte, 32)
	rand.Read(b)

	s := &Server{
		cfg:          cfg,
		password:     password,
		listenAddr:   listenAddr,
		sessions:     newSessionStore(),
		loginLimiter: newRateLimiter(),
		sseBroker:    NewSSEBroker(),
		apiKey:       hex.EncodeToString(b),
	}

	if err := s.loadTemplates(); err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}

	s.setupRouter()
	return s, nil
}

func (s *Server) loadTemplates() error {
	tmplFS, err := fs.Sub(webfs.Assets, "templates")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"join": strings.Join,
	}

	s.templates = make(map[string]*template.Template)

	pages := []string{
		"dashboard.html",
		"vms.html",
		"vm_create.html",
		"vm_detail.html",
		"clusters.html",
		"cluster_create.html",
		"api_key.html",
	}

	for _, page := range pages {
		t, err := template.New("").Funcs(funcMap).ParseFS(tmplFS, "layout.html", page)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", page, err)
		}
		s.templates[page] = t
	}

	// Standalone templates (no layout)
	for _, name := range []string{"login.html", "vm_row.html"} {
		t, err := template.New("").Funcs(funcMap).ParseFS(tmplFS, name)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", name, err)
		}
		s.templates[name] = t
	}

	return nil
}

func (s *Server) setupRouter() {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(s.securityHeaders)

	// Static files
	staticFS, _ := fs.Sub(webfs.Assets, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Public routes
	r.Get("/login", s.handleLoginPage)
	r.Post("/login", s.handleLogin)

	// Health check (no auth)
	r.Get("/api/v1/health", s.handleAPIHealth)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Use(s.csrfMiddleware)

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		})
		r.Post("/logout", s.handleLogout)

		// Dashboard
		r.Get("/dashboard", s.handleDashboard)

		// SSE events
		r.Get("/events", s.handleSSE)

		// API key page
		r.Get("/api-key", s.handleAPIKeyPage)

		// VM HTML routes
		r.Get("/vms", s.handleVMList)
		r.Get("/vms/new", s.handleVMCreateForm)
		r.Post("/vms", s.handleVMCreate)
		r.Get("/vms/{name}", s.handleVMDetail)
		r.Post("/vms/{name}/start", s.handleVMStart)
		r.Post("/vms/{name}/stop", s.handleVMStop)
		r.Delete("/vms/{name}", s.handleVMDelete)
		r.Post("/vms/{name}/delete", s.handleVMDeletePost)

		// Cluster HTML routes
		r.Get("/clusters", s.handleClusterList)
		r.Get("/clusters/new", s.handleClusterCreateForm)
		r.Post("/clusters", s.handleClusterCreate)
		r.Delete("/clusters/{name}", s.handleClusterDelete)
		r.Post("/clusters/{name}/delete", s.handleClusterDeletePost)

		// JSON API
		r.Route("/api/v1", func(r chi.Router) {
			r.Get("/vms", s.handleAPIVMList)
			r.Post("/vms", s.handleAPIVMCreate)
			r.Get("/vms/{name}", s.handleAPIVMDetail)
			r.Post("/vms/{name}/start", s.handleAPIVMStart)
			r.Post("/vms/{name}/stop", s.handleAPIVMStop)
			r.Delete("/vms/{name}", s.handleAPIVMDelete)

			r.Get("/clusters", s.handleAPIClusterList)
			r.Post("/clusters", s.handleAPIClusterCreate)
			r.Delete("/clusters/{name}", s.handleAPIClusterDelete)
		})
	})

	s.router = r
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAPIKeyPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "api_key.html", "api-key", map[string]interface{}{
		"APIKey":     s.apiKey,
		"ListenAddr": s.listenAddr,
	})
}

func (s *Server) Run() error {
	go s.sseBroker.Start(s.cfg)
	log.Printf("VMM Web UI listening on %s", s.listenAddr)
	return http.ListenAndServe(s.listenAddr, s.router)
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data map[string]interface{}) {
	t, ok := s.templates[name]
	if !ok {
		log.Printf("template %q not found", name)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, name string, active string, data map[string]interface{}) {
	data["Active"] = active

	if cookie, err := r.Cookie("vmm_session"); err == nil {
		data["CSRFToken"] = cookie.Value
	}

	t, ok := s.templates[name]
	if !ok {
		log.Printf("template %q not found", name)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
