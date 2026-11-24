package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Solidsilver/yoto-proxy/internal/navidrome"
	"github.com/Solidsilver/yoto-proxy/internal/store"
)

const (
	sessionCookie = "yp_session"
	flowCookie    = "yp_flow"
	sessionTTL    = 30 * 24 * time.Hour
)

type session struct {
	Sub  string `json:"sub"`
	Name string `json:"name"`
	User string `json:"user"` // login username (preferred_username), used to pick the Navidrome user
	Exp  int64  `json:"exp"`
}

// flow carries state between an OAuth redirect and its callback.
type flow struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce,omitempty"`
	Next     string `json:"next,omitempty"`
	Exp      int64  `json:"exp"`
}

// --- signed cookies ---

func (s *Server) sign(v any) string {
	payload, _ := json.Marshal(v)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.cookieKey)
	mac.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) verify(value string, v any) bool {
	p, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, s.cookieKey)
	mac.Write([]byte(p))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(p)
	return err == nil && json.Unmarshal(payload, v) == nil
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
}

func (s *Server) readFlow(r *http.Request) (*flow, bool) {
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return nil, false
	}
	var f flow
	if !s.verify(c.Value, &f) || time.Now().Unix() > f.Exp {
		return nil, false
	}
	return &f, true
}

func (s *Server) currentSession(r *http.Request) (*session, bool) {
	if s.cfg.AuthDisabled {
		return &session{Sub: "dev", Name: "Developer", User: "dev"}, true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	var sess session
	if !s.verify(c.Value, &sess) || time.Now().Unix() > sess.Exp || sess.User == "" {
		return nil, false // sessions from before usernames were recorded log in again
	}
	return &sess, true
}

// requireAuth guards the API and the Yoto connect flow.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentSession(r); !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusUnauthorized, "not logged in")
				return
			}
			http.Redirect(w, r, "/auth/login?next="+r.URL.Path, http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Pocket ID (OIDC) login ---

type oidcClient struct {
	mu       sync.Mutex
	provider *oidc.Provider
}

// get initializes the provider lazily so the server can start while the
// identity provider is still coming up.
func (s *Server) oidc(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	s.oidcState.mu.Lock()
	defer s.oidcState.mu.Unlock()
	if s.oidcState.provider == nil {
		p, err := oidc.NewProvider(ctx, s.cfg.OIDCIssuer)
		if err != nil {
			return nil, nil, err
		}
		s.oidcState.provider = p
	}
	p := s.oidcState.provider
	return p, &oauth2.Config{
		ClientID:     s.cfg.OIDCClientID,
		ClientSecret: s.cfg.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  s.cfg.PublicURL + "/auth/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}, nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if s.cfg.AuthDisabled {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	_, oc, err := s.oidc(r.Context())
	if err != nil {
		slog.Error("oidc discovery", "err", err)
		http.Error(w, "identity provider unavailable", http.StatusBadGateway)
		return
	}
	f := flow{State: store.RandomToken(16), Verifier: oauth2.GenerateVerifier(),
		Nonce: store.RandomToken(16), Next: next, Exp: time.Now().Add(10 * time.Minute).Unix()}
	s.setCookie(w, flowCookie, s.sign(f), 10*time.Minute)
	http.Redirect(w, r, oc.AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	f, ok := s.readFlow(r)
	if !ok || r.URL.Query().Get("state") != f.State {
		http.Error(w, "login expired, please try again", http.StatusBadRequest)
		return
	}
	s.clearCookie(w, flowCookie)
	p, oc, err := s.oidc(r.Context())
	if err != nil {
		http.Error(w, "identity provider unavailable", http.StatusBadGateway)
		return
	}
	tok, err := oc.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(f.Verifier))
	if err != nil {
		slog.Warn("oidc exchange", "err", err)
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: s.cfg.OIDCClientID}).Verify(r.Context(), raw)
	if err != nil || idt.Nonce != f.Nonce {
		slog.Warn("oidc id token", "err", err)
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	_ = idt.Claims(&claims)
	name := firstNonEmpty(claims.Name, claims.PreferredUsername, claims.Email, idt.Subject)
	user := firstNonEmpty(claims.PreferredUsername, claims.Email, idt.Subject)
	s.setCookie(w, sessionCookie, s.sign(session{Sub: idt.Subject, Name: name, User: user,
		Exp: time.Now().Add(sessionTTL).Unix()}), sessionTTL)
	http.Redirect(w, r, f.Next, http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- Yoto account connection ---

func (s *Server) handleYotoLogin(w http.ResponseWriter, r *http.Request) {
	f := flow{State: store.RandomToken(16), Verifier: oauth2.GenerateVerifier(),
		Next: "/", Exp: time.Now().Add(10 * time.Minute).Unix()}
	s.setCookie(w, flowCookie, s.sign(f), 10*time.Minute)
	http.Redirect(w, r, s.yoto.AuthURL(f.State, f.Verifier), http.StatusFound)
}

// navUser maps a login username to the Navidrome username it acts as.
func (s *Server) navUser(sess *session) string { return s.navName(sess.User) }

func (s *Server) navName(login string) string {
	if u, ok := s.cfg.NavidromeUserMap[login]; ok {
		return u
	}
	return login
}

// navAs returns a Navidrome client acting as a login user (e.g. a card owner).
func (s *Server) navAs(login string) *navidrome.Client { return s.nav.As(s.navName(login)) }

// navFor returns a Navidrome client acting as the logged-in user.
func (s *Server) navFor(r *http.Request) *navidrome.Client {
	sess, _ := s.currentSession(r)
	if sess == nil {
		return s.nav.As("")
	}
	return s.nav.As(s.navUser(sess))
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
