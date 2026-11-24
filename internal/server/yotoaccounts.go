package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Solidsilver/yoto-proxy/internal/store"
	"github.com/Solidsilver/yoto-proxy/internal/yoto"
)

// Each logged-in user belongs to at most one Yoto account, which several
// users can share (by invite, or by connecting the same Yoto account).
// Cards belong to a Yoto account; members see and manage its cards, while
// each card still streams with its creator's Navidrome/Audiobookshelf access.

const inviteTTL = 7 * 24 * time.Hour

var errNoYotoAccount = errors.New("Connect a Yoto account first")

// tokenStore keeps one Yoto account's tokens in yoto_accounts.tokens.
type tokenStore struct {
	st *store.Store
	id int64
}

func (t tokenStore) LoadTokens(ctx context.Context) (*yotoTokens, error) {
	a, err := t.st.GetYotoAccount(ctx, t.id)
	if err != nil {
		return nil, err
	}
	if a.Tokens == "" {
		return nil, nil
	}
	var tok yotoTokens
	if err := json.Unmarshal([]byte(a.Tokens), &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func (t tokenStore) SaveTokens(ctx context.Context, tok *yotoTokens) error {
	b, _ := json.Marshal(tok)
	return t.st.SetYotoTokens(ctx, t.id, string(b))
}

// yotoAcct returns the (cached) API client for a Yoto account. Caching keeps
// one refresh mutex per account.
func (s *Server) yotoAcct(id int64) *yoto.Account {
	s.yotoMu.Lock()
	defer s.yotoMu.Unlock()
	if a, ok := s.yotoAccts[id]; ok {
		return a
	}
	a := s.yoto.Account(tokenStore{s.store, id})
	s.yotoAccts[id] = a
	return a
}

// userAccount returns the Yoto account id of the logged-in user.
func (s *Server) userAccount(r *http.Request) (int64, error) {
	sess, _ := s.currentSession(r)
	if sess == nil {
		return 0, errNoYotoAccount
	}
	id, err := s.store.MemberAccount(r.Context(), sess.User)
	if errors.Is(err, store.ErrNotFound) {
		return 0, errNoYotoAccount
	}
	return id, err
}

// cardFor loads a card if it belongs to the user's Yoto account; otherwise
// it writes a 404 and returns nil.
func (s *Server) cardFor(w http.ResponseWriter, r *http.Request) *store.Card {
	id, ok := cardID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return nil
	}
	acct, err := s.userAccount(r)
	card, cerr := s.store.GetCard(r.Context(), id)
	if err != nil || cerr != nil || card.YotoAccountID != acct {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	return card
}

// migrateLegacyYoto turns the single Yoto connection of earlier versions
// into a Yoto account whose members are the existing cards' owners.
func (s *Server) migrateLegacyYoto(ctx context.Context) error {
	v, err := s.store.GetSetting(ctx, "yoto_tokens")
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if n, err := s.store.CountYotoAccounts(ctx); err != nil || n > 0 {
		return err
	}
	var tok yotoTokens
	_ = json.Unmarshal([]byte(v), &tok)
	id, err := s.store.CreateYotoAccount(ctx, yoto.Subject(&tok), "Family Yoto", v, "")
	if err != nil {
		return err
	}
	if err := s.store.AssignUnownedCards(ctx, id); err != nil {
		return err
	}
	owners, err := s.store.CardOwners(ctx, id)
	if err != nil {
		return err
	}
	for _, o := range owners {
		if err := s.store.SetMember(ctx, o, id); err != nil {
			return err
		}
	}
	slog.Info("migrated legacy Yoto connection", "account", id, "members", owners)
	return s.store.DeleteSetting(ctx, "yoto_tokens")
}

// handleYotoCallback finishes the Yoto login. Connecting a Yoto account that
// someone already connected joins it; otherwise a new one is created.
func (s *Server) handleYotoCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		http.Error(w, "Yoto login failed: "+e+" "+q.Get("error_description"), http.StatusBadRequest)
		return
	}
	f, ok := s.readFlow(r)
	if !ok || q.Get("state") != f.State {
		http.Error(w, "Yoto login expired, please try again", http.StatusBadRequest)
		return
	}
	s.clearCookie(w, flowCookie)
	ctx := r.Context()
	tok, err := s.yoto.Exchange(ctx, q.Get("code"), f.Verifier)
	if err != nil {
		slog.Error("yoto exchange", "err", err)
		http.Error(w, "Yoto login failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	sess, _ := s.currentSession(r)
	tokJSON, _ := json.Marshal(tok)
	sub := yoto.Subject(tok)

	var id int64
	if existing, err := s.store.GetYotoAccountBySubject(ctx, sub); err == nil {
		id = existing.ID
		err = s.store.SetYotoTokens(ctx, id, string(tokJSON))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		id, err = s.store.CreateYotoAccount(ctx, sub, firstNonEmpty(sess.Name, sess.User)+"’s Yoto", string(tokJSON), sess.User)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := s.store.SetMember(ctx, sess.User, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("yoto connected", "user", sess.User, "account", id)
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleYotoLeave removes the user from their Yoto account. When the last
// member leaves, its tokens are forgotten; cards keep playing, and
// reconnecting the same Yoto account brings them back.
func (s *Server) handleYotoLeave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, _ := s.currentSession(r)
	id, err := s.userAccount(r)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.RemoveMember(ctx, sess.User); err != nil {
		writeErr(w, err)
		return
	}
	if members, err := s.store.ListMembers(ctx, id); err == nil && len(members) == 0 {
		_ = s.store.SetYotoTokens(ctx, id, "")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleYotoRename(w http.ResponseWriter, r *http.Request) {
	id, err := s.userAccount(r)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Label) == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	if err := s.store.SetYotoLabel(r.Context(), id, strings.TrimSpace(in.Label)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// yotoInfo describes the user's Yoto account for /api/me.
func (s *Server) yotoInfo(ctx context.Context, r *http.Request) map[string]any {
	id, err := s.userAccount(r)
	if err != nil {
		return map[string]any{"connected": false}
	}
	acct, err := s.store.GetYotoAccount(ctx, id)
	if err != nil {
		return map[string]any{"connected": false}
	}
	members, _ := s.store.ListMembers(ctx, id)
	return map[string]any{
		"connected": s.yotoAcct(id).Connected(ctx),
		"label":     acct.Label,
		"members":   members,
	}
}

// --- invites ---

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	id, err := s.userAccount(r)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	sess, _ := s.currentSession(r)
	inv, err := s.store.CreateInvite(r.Context(), id, firstNonEmpty(sess.Name, sess.User), inviteTTL)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"url":       s.cfg.PublicURL + "/invite/" + inv.Token,
		"expiresAt": inv.ExpiresAt,
	})
}

func (s *Server) handleGetInvite(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.GetInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "This invite link has expired or was already used.")
		return
	}
	acct, err := s.store.GetYotoAccount(r.Context(), inv.AccountID)
	if err != nil {
		writeError(w, http.StatusNotFound, "This invite link has expired or was already used.")
		return
	}
	members, _ := s.store.ListMembers(r.Context(), inv.AccountID)
	current, _ := s.userAccount(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"label":         acct.Label,
		"invitedBy":     inv.CreatedBy,
		"members":       members,
		"alreadyMember": current == inv.AccountID,
		"inOtherYoto":   current != 0 && current != inv.AccountID,
	})
}

func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inv, err := s.store.GetInvite(ctx, r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "This invite link has expired or was already used.")
		return
	}
	sess, _ := s.currentSession(r)
	if err := s.store.SetMember(ctx, sess.User, inv.AccountID); err != nil {
		writeErr(w, err)
		return
	}
	_ = s.store.DeleteInvite(ctx, inv.Token)
	slog.Info("invite accepted", "user", sess.User, "account", inv.AccountID)
	w.WriteHeader(http.StatusNoContent)
}
