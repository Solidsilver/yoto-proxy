package server

import (
	"errors"
	"os"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Solidsilver/yoto-proxy/internal/navidrome"
	"github.com/Solidsilver/yoto-proxy/internal/store"
)

// handleStream serves /s/{token}/{key}.mp3 to the Yoto player. The token is
// the only credential, so it is per card, random, and rotatable.
//
// mp3 sources are passed through untouched (Navidrome's raw stream supports
// Range and Content-Length); anything else is transcoded to mp3.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	card, err := s.store.GetCardByToken(ctx, r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	key := strings.TrimSuffix(r.PathValue("file"), ".mp3")
	track, err := s.store.GetTrack(ctx, card.ID, key)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Log who fetches and how: this tells us whether the player or Yoto's
	// cloud pulls the audio, and whether it seeks with Range.
	slog.Info("stream", "card", card.ID, "track", key, "method", r.Method,
		"range", r.Header.Get("Range"), "ua", r.UserAgent(),
		"client", firstNonEmpty(r.Header.Get("X-Forwarded-For"), r.RemoteAddr))

	var resp *http.Response
	raw := false
	switch {
	case isNavidrome(card.SourceType):
		opts := navidrome.StreamOptions{
			Raw:     strings.EqualFold(track.Suffix, "mp3"),
			Bitrate: s.cfg.TranscodeBitrate,
			Range:   r.Header.Get("Range"),
			IfRange: r.Header.Get("If-Range"),
		}
		raw = opts.Raw
		resp, err = s.navAs(card.Owner).Stream(ctx, track.SourceTrackID, opts)

	case card.SourceType == store.SourceABSItem && track.Mode == store.ModeCut:
		s.serveCut(w, r, card, track)
		return

	case card.SourceType == store.SourceABSItem && s.abs != nil:
		// Whole mp3 file: pass through with Range.
		var token string
		if token, err = s.absToken(ctx, card.Owner); err == nil {
			raw = true
			resp, err = s.abs.File(ctx, token, card.SourceID, track.SourceTrackID,
				r.Header.Get("Range"), r.Header.Get("If-Range"))
		}

	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		if !errors.Is(err, ctx.Err()) {
			slog.Error("stream upstream", "err", err)
		}
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Subsonic reports errors as a 200 with an XML/JSON body.
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 400 || strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/xml") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		slog.Error("stream upstream error", "status", resp.StatusCode, "body", string(body))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "audio/mpeg")
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if !raw {
		h.Set("Accept-Ranges", "none")
	}
	h.Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil && ctx.Err() == nil {
		slog.Warn("stream copy", "err", err)
	}
}

// serveCut serves a chapter cut from an Audiobookshelf file: from the cache
// when rendered (with Range), otherwise rendered live by ffmpeg.
func (s *Server) serveCut(w http.ResponseWriter, r *http.Request, card *store.Card, t *store.Track) {
	if f, err := os.Open(s.cache.path(card, t)); err == nil {
		defer f.Close()
		st, err := f.Stat()
		if err == nil {
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("Cache-Control", "private, max-age=3600")
			http.ServeContent(w, r, "", st.ModTime(), f)
			return
		}
	}
	s.cache.Enqueue(card.ID)
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	if err := s.cache.streamLive(r.Context(), w, card, t); err != nil {
		slog.Error("live cut", "card", card.ID, "track", t.Key, "err", err)
	}
}
