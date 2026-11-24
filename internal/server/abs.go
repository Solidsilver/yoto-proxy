package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/Solidsilver/yoto-proxy/internal/abs"
	"github.com/Solidsilver/yoto-proxy/internal/store"
)

var (
	errABSDisabled     = errors.New("Audiobookshelf is not configured")
	errABSNotConnected = errors.New("Audiobookshelf account not connected")
)

// absState holds in-flight logins and serializes token refreshes.
type absState struct {
	mu    sync.Mutex
	flows map[string]*absFlow // by state
}

type absFlow struct {
	flow      *abs.LoginFlow
	loginUser string
	expires   time.Time
}

func (s *Server) absCallbackURL() string { return s.cfg.PublicURL + "/auth/abs/callback" }

// handleABSLogin starts Audiobookshelf's OpenID (mobile-app) flow on the
// server and sends the browser to the identity provider.
func (s *Server) handleABSLogin(w http.ResponseWriter, r *http.Request) {
	if s.abs == nil {
		http.Error(w, errABSDisabled.Error(), http.StatusNotFound)
		return
	}
	sess, _ := s.currentSession(r)
	state := store.RandomToken(16)
	authURL, flow, err := s.abs.StartLogin(r.Context(), s.absCallbackURL(), state, oauth2.GenerateVerifier())
	if err != nil {
		slog.Error("abs login start", "err", err)
		http.Error(w, "Could not start the Audiobookshelf login: "+err.Error()+
			"\n\nCheck that "+s.absCallbackURL()+" is listed under Settings → Authentication → "+
			"Allowed Mobile Redirect URIs in Audiobookshelf.", http.StatusBadGateway)
		return
	}
	s.absState.mu.Lock()
	for k, f := range s.absState.flows {
		if time.Now().After(f.expires) {
			delete(s.absState.flows, k)
		}
	}
	s.absState.flows[state] = &absFlow{flow: flow, loginUser: sess.User, expires: time.Now().Add(10 * time.Minute)}
	s.absState.mu.Unlock()
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleABSCallback(w http.ResponseWriter, r *http.Request) {
	if s.abs == nil {
		http.Error(w, errABSDisabled.Error(), http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	sess, _ := s.currentSession(r)
	s.absState.mu.Lock()
	f := s.absState.flows[q.Get("state")]
	delete(s.absState.flows, q.Get("state"))
	s.absState.mu.Unlock()
	if f == nil || time.Now().After(f.expires) || f.loginUser != sess.User {
		http.Error(w, "Audiobookshelf login expired, please try again", http.StatusBadRequest)
		return
	}
	user, err := s.abs.FinishLogin(r.Context(), f.flow, q.Get("code"))
	if err != nil {
		slog.Error("abs login finish", "err", err)
		http.Error(w, "Audiobookshelf login failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	acct := &store.ABSAccount{
		LoginUser: sess.User, ABSUserID: user.ID, ABSUsername: user.Username, ABSType: user.Type,
		Kind: store.ABSKindSession, Token: user.AccessToken, RefreshToken: user.RefreshToken,
	}
	// Admins can mint a non-expiring API key for themselves, which keeps
	// cards playing even if nobody opens this app for months.
	if user.IsAdmin() {
		key, err := s.abs.CreateAPIKey(r.Context(), user.AccessToken, user.ID,
			"yoto-proxy ("+time.Now().Format("2006-01-02")+")")
		if err != nil {
			slog.Warn("abs api key creation failed; using refresh tokens", "user", user.Username, "err", err)
		} else {
			acct.Kind, acct.Token, acct.RefreshToken = store.ABSKindAPIKey, key, ""
		}
	}
	if err := s.store.SaveABSAccount(r.Context(), acct); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("abs connected", "login", sess.User, "absUser", user.Username, "kind", acct.Kind)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleABSDisconnect(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	if err := s.store.DeleteABSAccount(r.Context(), sess.User); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// absToken returns a usable Audiobookshelf token for loginUser, refreshing
// session tokens that are about to expire.
func (s *Server) absToken(ctx context.Context, loginUser string) (string, error) {
	s.absState.mu.Lock()
	defer s.absState.mu.Unlock()
	acct, err := s.store.GetABSAccount(ctx, loginUser)
	if errors.Is(err, store.ErrNotFound) {
		return "", errABSNotConnected
	}
	if err != nil {
		return "", err
	}
	if acct.Kind == store.ABSKindAPIKey || time.Until(jwtExpiry(acct.Token)) > 5*time.Minute {
		return acct.Token, nil
	}
	return s.refreshABSLocked(ctx, acct)
}

func (s *Server) refreshABSLocked(ctx context.Context, acct *store.ABSAccount) (string, error) {
	u, err := s.abs.Refresh(ctx, acct.RefreshToken)
	if err != nil {
		return "", errors.Join(errABSNotConnected, err)
	}
	acct.Token, acct.RefreshToken = u.AccessToken, u.RefreshToken
	if err := s.store.SaveABSAccount(ctx, acct); err != nil {
		return "", err
	}
	return acct.Token, nil
}

// keepABSSessionsAlive refreshes session-based accounts daily so their
// 30-day refresh tokens never lapse while cards are in use.
func (s *Server) keepABSSessionsAlive(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		accts, err := s.store.ListABSAccounts(ctx)
		if err == nil {
			for i := range accts {
				a := &accts[i]
				if a.Kind != store.ABSKindSession {
					continue
				}
				s.absState.mu.Lock()
				_, err := s.refreshABSLocked(ctx, a)
				s.absState.mu.Unlock()
				if err != nil {
					slog.Warn("abs session refresh failed", "login", a.LoginUser, "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// jwtExpiry reads the exp claim without verifying (the token is ours).
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Now().Add(24 * time.Hour) // non-expiring legacy token
	}
	return time.Unix(claims.Exp, 0)
}

// --- browsing ---

func (s *Server) absFor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.abs == nil {
		writeError(w, http.StatusNotFound, errABSDisabled.Error())
		return "", false
	}
	sess, _ := s.currentSession(r)
	token, err := s.absToken(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return "", false
	}
	return token, true
}

func (s *Server) handleABSLibraries(w http.ResponseWriter, r *http.Request) {
	token, ok := s.absFor(w, r)
	if !ok {
		return
	}
	libs, err := s.abs.Libraries(r.Context(), token)
	if err != nil {
		writeErr(w, err)
		return
	}
	books := []abs.Library{}
	for _, l := range libs {
		if l.MediaType == "book" {
			books = append(books, l)
		}
	}
	writeJSON(w, http.StatusOK, books)
}

func (s *Server) handleABSItems(w http.ResponseWriter, r *http.Request) {
	token, ok := s.absFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	items, total, err := s.abs.Items(r.Context(), token, r.PathValue("id"), strings.TrimSpace(q.Get("q")), page, 60)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// handleABSItem returns an item and the tracks its card would get.
func (s *Server) handleABSItem(w http.ResponseWriter, r *http.Request) {
	token, ok := s.absFor(w, r)
	if !ok {
		return
	}
	item, err := s.abs.Item(r.Context(), token, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	segs, err := abs.Plan(item)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	type planned struct {
		Title    string  `json:"title"`
		Duration float64 `json:"duration"`
		Cached   bool    `json:"needsCache"`
	}
	tracks := make([]planned, len(segs))
	for i, sg := range segs {
		tracks[i] = planned{Title: sg.Title, Duration: sg.Duration(), Cached: !(sg.Whole && sg.Ext == "mp3")}
	}
	item.Media.AudioFiles, item.Media.Chapters = nil, nil
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "tracks": tracks})
}

func (s *Server) handleABSCover(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	s.serveCover(w, r, sess.User, store.SourceABSItem, r.PathValue("id"))
}
