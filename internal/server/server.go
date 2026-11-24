// Package server wires the HTTP routes: the authenticated admin API and UI,
// the Yoto/Pocket ID OAuth flows, and the public stream endpoint the Yoto
// player fetches audio from.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Solidsilver/yoto-proxy/internal/abs"
	"github.com/Solidsilver/yoto-proxy/internal/config"
	"github.com/Solidsilver/yoto-proxy/internal/navidrome"
	"github.com/Solidsilver/yoto-proxy/internal/store"
	"github.com/Solidsilver/yoto-proxy/internal/yoto"
)

type (
	yotoTokens = yoto.Tokens
	navSong    = navidrome.Song
)

type Server struct {
	cfg           *config.Config
	store         *store.Store
	nav           *navidrome.Client
	abs           *abs.Client // nil when Audiobookshelf is not configured
	absState      absState
	cache         *chapterCache
	yotoMu        sync.Mutex
	yotoAccts     map[int64]*yoto.Account
	yoto          *yoto.Client
	cookieKey     []byte
	secureCookies bool
	oidcState     oidcClient
	syncMu        sync.Mutex // one sync at a time keeps Yoto calls simple
	static        fs.FS
}

func New(ctx context.Context, cfg *config.Config, st *store.Store, static fs.FS) (*Server, error) {
	key, err := st.SecretSetting(ctx, "cookie_key")
	if err != nil {
		return nil, err
	}
	nav, err := navidrome.New(navidrome.Options{
		BaseURL:    cfg.NavidromeURL,
		User:       cfg.NavidromeUser,
		Password:   cfg.NavidromePassword,
		HeaderAuth: cfg.NavidromeAuth == "header",
		UserHeader: cfg.NavidromeHeader,
		SourceAddr: cfg.NavidromeSource,
	})
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:           cfg,
		store:         st,
		nav:           nav,
		cookieKey:     key,
		secureCookies: strings.HasPrefix(cfg.PublicURL, "https://"),
		static:        static,
	}
	s.yoto = yoto.New(cfg.YotoClientID, cfg.YotoClientSecret, cfg.PublicURL+"/auth/yoto/callback")
	s.yotoAccts = map[int64]*yoto.Account{}
	if err := s.migrateLegacyYoto(ctx); err != nil {
		return nil, fmt.Errorf("migrate yoto connection: %w", err)
	}
	s.absState.flows = map[string]*absFlow{}
	s.cache = newChapterCache(s, filepath.Join(cfg.DataDir, "cache"))
	go s.cache.Run(ctx)
	if cfg.ABSPublicURL != "" {
		s.abs = abs.New(cfg.ABSURL, cfg.ABSPublicURL)
		go s.keepABSSessionsAlive(ctx)
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public: health and audio streams for the Yoto player.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /s/{token}/{file}", s.handleStream)
	mux.HandleFunc("HEAD /s/{token}/{file}", s.handleStream)
	for _, f := range publicAssets {
		mux.Handle("GET /"+f, s.spaHandler())
	}

	// Pocket ID login.
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)

	// Everything below requires a session.
	authed := http.NewServeMux()
	authed.HandleFunc("GET /auth/yoto/login", s.handleYotoLogin)
	authed.HandleFunc("GET /auth/yoto/callback", s.handleYotoCallback)
	authed.HandleFunc("POST /api/yoto/leave", s.handleYotoLeave)
	authed.HandleFunc("PATCH /api/yoto", s.handleYotoRename)
	authed.HandleFunc("POST /api/yoto/invites", s.handleCreateInvite)
	authed.HandleFunc("GET /api/invites/{token}", s.handleGetInvite)
	authed.HandleFunc("POST /api/invites/{token}/accept", s.handleAcceptInvite)
	authed.HandleFunc("GET /auth/abs/login", s.handleABSLogin)
	authed.HandleFunc("GET /auth/abs/callback", s.handleABSCallback)
	authed.HandleFunc("POST /api/abs/disconnect", s.handleABSDisconnect)
	authed.HandleFunc("GET /api/abs/libraries", s.handleABSLibraries)
	authed.HandleFunc("GET /api/abs/libraries/{id}/items", s.handleABSItems)
	authed.HandleFunc("GET /api/abs/items/{id}", s.handleABSItem)
	authed.HandleFunc("GET /api/abs/cover/{id}", s.handleABSCover)
	authed.HandleFunc("GET /api/me", s.handleMe)
	authed.HandleFunc("GET /api/navidrome/playlists", s.handlePlaylists)
	authed.HandleFunc("GET /api/navidrome/playlists/{id}", s.handlePlaylist)
	authed.HandleFunc("GET /api/navidrome/albums", s.handleAlbums)
	authed.HandleFunc("GET /api/navidrome/albums/{id}", s.handleAlbum)
	authed.HandleFunc("GET /api/navidrome/cover/{id}", s.handleCover)
	authed.HandleFunc("GET /api/cards/{id}/cover", s.handleCardCover)
	authed.HandleFunc("GET /api/cards", s.handleListCards)
	authed.HandleFunc("POST /api/cards", s.handleCreateCard)
	authed.HandleFunc("GET /api/cards/{id}", s.handleGetCard)
	authed.HandleFunc("PATCH /api/cards/{id}", s.handleUpdateCard)
	authed.HandleFunc("DELETE /api/cards/{id}", s.handleDeleteCard)
	authed.HandleFunc("POST /api/cards/{id}/sync", s.handleSyncCard)
	authed.HandleFunc("POST /api/cards/{id}/rotate-token", s.handleRotateToken)
	authed.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	authed.Handle("/", s.spaHandler())
	mux.Handle("/", s.requireAuth(authed))

	return logRequests(mux)
}

// publicAssets are served without login: browsers and iOS fetch icons and
// the manifest outside the user's session.
var publicAssets = []string{
	"favicon.ico", "favicon-32.png", "favicon-64.png", "apple-touch-icon.png",
	"apple-touch-icon-precomposed.png", "icon-192.png", "icon-512.png", "logo.png",
	"manifest.webmanifest", "robots.txt",
}

func init() { _ = mime.AddExtensionType(".webmanifest", "application/manifest+json") }

// spaHandler serves the embedded SvelteKit build, falling back to index.html.
func (s *Server) spaHandler() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := s.static.Open(p); err == nil {
				f.Close()
				if strings.HasPrefix(p, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.static, "index.html")
		if err != nil {
			http.Error(w, "frontend not built (run `pnpm build` in web/)", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErr maps internal errors to HTTP statuses.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, yoto.ErrNotConnected):
		writeError(w, http.StatusConflict, "Yoto account not connected")
	case errors.Is(err, errABSNotConnected), errors.Is(err, errABSDisabled), errors.Is(err, errNoYotoAccount):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("request failed", "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/_app/") {
			return
		}
		path := r.URL.Path
		if strings.HasPrefix(path, "/s/") {
			path = "/s/…/" + r.PathValue("file") // never log stream tokens
		}
		slog.Info("http", "method", r.Method, "path", path, "status", rec.status,
			"bytes", rec.bytes, "dur", time.Since(start).Round(time.Millisecond))
	})
}
