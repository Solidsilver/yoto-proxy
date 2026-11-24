package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Solidsilver/yoto-proxy/internal/cover"
	"github.com/Solidsilver/yoto-proxy/internal/store"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	navErr := ""
	if err := s.navFor(r).Ping(r.Context()); err != nil {
		navErr = err.Error()
	}
	navUser := s.cfg.NavidromeUser
	if s.nav.HeaderAuth() {
		navUser = s.navUser(sess)
	}
	absInfo := map[string]any{"configured": s.abs != nil, "connected": false}
	if s.abs != nil {
		if a, err := s.store.GetABSAccount(r.Context(), sess.User); err == nil {
			absInfo["connected"] = true
			absInfo["username"] = a.ABSUsername
			absInfo["kind"] = a.Kind
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":           sess.Name,
		"user":           sess.User,
		"navidromeUser":  navUser,
		"audiobookshelf": absInfo,
		"yoto":           s.yotoInfo(r.Context(), r),
		"navidromeOk":    navErr == "",
		"navidromeError": navErr,
		"authDisabled":   s.cfg.AuthDisabled,
	})
}

// --- Navidrome browsing ---

func (s *Server) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	pls, err := s.navFor(r).Playlists(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pls)
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	p, err := s.navFor(r).Playlist(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleAlbums(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 || size > 500 {
		size = 100
	}
	albums, err := s.navFor(r).Albums(r.Context(), strings.TrimSpace(q.Get("q")), offset, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, albums)
}

func (s *Server) handleAlbum(w http.ResponseWriter, r *http.Request) {
	a, err := s.navFor(r).Album(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// handleCover proxies Navidrome cover art; ?portrait=1 renders it as Yoto card art.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	s.serveCover(w, r, actor(s, r), store.SourceNavidromeAlbum, r.PathValue("id"))
}

// handleCardCover shows a card's cover as it appears on the Yoto card.
func (s *Server) handleCardCover(w http.ResponseWriter, r *http.Request) {
	card := s.cardFor(w, r)
	if card == nil || card.CoverArtID == "" {
		http.NotFound(w, r)
		return
	}
	owner := card.Owner
	if owner == "" {
		owner = actor(s, r)
	}
	q := r.URL.Query()
	q.Set("portrait", "1")
	r.URL.RawQuery = q.Encode()
	s.serveCover(w, r, owner, card.SourceType, card.CoverArtID)
}

// serveCover serves a source's cover; ?portrait=1 renders Yoto card art.
func (s *Server) serveCover(w http.ResponseWriter, r *http.Request, owner, sourceType, coverID string) {
	portrait := r.URL.Query().Get("portrait") == "1"
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 || size > 1200 {
		size = 300
	}
	if portrait {
		size = 640
	}
	body, err := s.openCover(r.Context(), owner, sourceType, coverID, size)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer body.Close()
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if portrait {
		img, err := cover.Portrait(body)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(img)
		return
	}
	io.Copy(w, body)
}

// actor is the logged-in username; cards record it as their owner.
func actor(s *Server, r *http.Request) string {
	sess, _ := s.currentSession(r)
	if sess == nil {
		return ""
	}
	return sess.User
}

// --- cards ---

// cardView is a card plus derived state for the UI.
type cardView struct {
	store.Card
	Cache *cacheProgress `json:"cache,omitempty"`
}

func (s *Server) view(ctx context.Context, c *store.Card) cardView {
	return cardView{Card: *c, Cache: s.cache.Progress(ctx, c)}
}

func cardID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *Server) handleListCards(w http.ResponseWriter, r *http.Request) {
	acct, err := s.userAccount(r)
	if err != nil {
		writeJSON(w, http.StatusOK, []cardView{})
		return
	}
	cards, err := s.store.ListCards(r.Context(), acct)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]cardView, len(cards))
	for i := range cards {
		out[i] = s.view(r.Context(), &cards[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCard(w http.ResponseWriter, r *http.Request) {
	c := s.cardFor(w, r)
	if c == nil {
		return
	}
	id := c.ID
	card, err := s.store.GetCard(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	tracks, err := s.store.ListTracks(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": card, "tracks": tracks})
}

// handleCreateCard records a card for a Navidrome playlist or album and
// immediately syncs it to Yoto.
func (s *Server) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SourceType string `json:"sourceType"`
		SourceID   string `json:"sourceId"`
		Title      string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !validSourceType(in.SourceType) {
		writeError(w, http.StatusBadRequest, "unsupported sourceType")
		return
	}
	if in.SourceID == "" {
		writeError(w, http.StatusBadRequest, "sourceId is required")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		src, err := s.loadSource(r.Context(), actor(s, r), in.SourceType, in.SourceID)
		if err != nil {
			writeErr(w, err)
			return
		}
		title = src.Title
	}
	acct, err := s.userAccount(r)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	card, err := s.store.CreateCard(r.Context(), acct, title, in.SourceType, in.SourceID, actor(s, r))
	if err != nil {
		writeErr(w, err)
		return
	}
	synced, err := s.syncCard(r.Context(), card.ID, actor(s, r))
	if err != nil {
		// Keep the card; the UI shows lastError and offers a retry.
		slog.Warn("initial sync failed", "card", card.ID, "err", err)
		card, _ = s.store.GetCard(r.Context(), card.ID)
		writeJSON(w, http.StatusCreated, s.view(r.Context(), card))
		return
	}
	writeJSON(w, http.StatusCreated, s.view(r.Context(), synced))
}

func (s *Server) handleUpdateCard(w http.ResponseWriter, r *http.Request) {
	c := s.cardFor(w, r)
	if c == nil {
		return
	}
	id := c.ID
	var in struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if err := s.store.UpdateCardTitle(r.Context(), id, strings.TrimSpace(in.Title)); err != nil {
		writeErr(w, err)
		return
	}
	card, err := s.syncCard(r.Context(), id, actor(s, r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), card))
}

func (s *Server) handleSyncCard(w http.ResponseWriter, r *http.Request) {
	c := s.cardFor(w, r)
	if c == nil {
		return
	}
	id := c.ID
	card, err := s.syncCard(r.Context(), id, actor(s, r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), card))
}

// handleRotateToken invalidates the card's stream URLs and re-syncs so Yoto
// gets the new ones.
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	c := s.cardFor(w, r)
	if c == nil {
		return
	}
	id := c.ID
	if err := s.store.RotateStreamToken(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	card, err := s.syncCard(r.Context(), id, actor(s, r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(r.Context(), card))
}

// handleDeleteCard removes the card locally and, unless ?keepYoto=1, deletes
// the Yoto playlist too.
func (s *Server) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	c := s.cardFor(w, r)
	if c == nil {
		return
	}
	id := c.ID
	card, err := s.store.GetCard(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if card.YotoCardID != "" && r.URL.Query().Get("keepYoto") != "1" {
		if err := s.yotoAcct(card.YotoAccountID).DeleteContent(r.Context(), card.YotoCardID); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := s.store.DeleteCard(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.cache.RemoveSource(r.Context(), card.SourceType, card.SourceID)
	w.WriteHeader(http.StatusNoContent)
}
