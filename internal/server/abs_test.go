package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Solidsilver/yoto-proxy/internal/config"
	"github.com/Solidsilver/yoto-proxy/internal/store"
	"github.com/Solidsilver/yoto-proxy/internal/yoto"
)

// fakeABS mimics the Audiobookshelf endpoints yoto-proxy uses. The book has
// two files: "a.mp3" (one chapter, whole file) and "b.m4b" (two chapters).
func fakeABS(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/auth/openid":
			q := r.URL.Query()
			if q.Get("code_challenge") == "" || q.Get("redirect_uri") != "https://yoto.example/auth/abs/callback" {
				t.Errorf("bad /auth/openid params: %v", q)
			}
			http.SetCookie(w, &http.Cookie{Name: "connect.sid", Value: "sess-1", Path: "/"})
			http.Redirect(w, r, "https://idp.example/authorize?state="+q.Get("state"), http.StatusFound)
		case r.URL.Path == "/auth/openid/callback":
			if c, err := r.Cookie("connect.sid"); err != nil || c.Value != "sess-1" || r.URL.Query().Get("code_verifier") == "" {
				t.Errorf("callback missing session cookie or verifier")
			}
			io.WriteString(w, `{"user":{"id":"u1","username":"luke","type":"root","accessToken":"access","refreshToken":"refresh"}}`)
		case r.URL.Path == "/api/api-keys" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if auth != "Bearer access" || body["userId"] != "u1" || body["isActive"] != true {
				t.Errorf("bad api-key request: %s %v", auth, body)
			}
			io.WriteString(w, `{"apiKey":{"apiKey":"the-key"}}`)
		case auth != "Bearer the-key":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/api/items/book1":
			io.WriteString(w, `{"id":"book1","updatedAt":5,"media":{"metadata":{"title":"Winnie","authorName":"Milne"},
				"audioFiles":[{"index":1,"ino":"a","duration":100,"metadata":{"filename":"a.mp3","ext":".mp3"}},
				              {"index":2,"ino":"b","duration":200,"metadata":{"filename":"b.m4b","ext":".m4b"}}],
				"chapters":[{"id":0,"start":0,"end":100,"title":"Ch 1"},{"id":1,"start":100,"end":180,"title":"Ch 2"},
				            {"id":2,"start":180,"end":300,"title":"Ch 3"}]}}`)
		case r.URL.Path == "/api/items/book1/cover":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngBytes())
		case r.URL.Path == "/api/items/book1/file/a":
			http.ServeContent(w, r, "a.mp3", time.Unix(0, 0), bytes.NewReader(mp3Bytes))
		default:
			t.Errorf("unexpected ABS call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return srv
}

// fakeFFmpeg writes "cut:<-ss>:<-t>" to its output (file or pipe:1).
func fakeFFmpeg(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
ss=""; dur=""; out=""
while [ $# -gt 0 ]; do
  case "$1" in -ss) ss=$2; shift;; -t) dur=$2; shift;; esac
  out=$1; shift
done
if [ "$out" = "pipe:1" ]; then printf "cut:%s:%s" "$ss" "$dur"; else printf "cut:%s:%s" "$ss" "$dur" > "$out"; fi
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAudiobookshelfCard(t *testing.T) {
	absSrv := fakeABS(t)
	defer absSrv.Close()
	fy := newFakeYoto(t)
	defer fy.srv.Close()
	yoto.APIBase = fy.srv.URL

	cfg := &config.Config{
		PublicURL: "https://yoto.example", DataDir: t.TempDir(), NavidromeAuth: "header",
		ABSURL: absSrv.URL, ABSPublicURL: absSrv.URL, ABSBitrate: 64, FFmpegPath: fakeFFmpeg(t),
		YotoClientID: "cid", AuthDisabled: true,
	}
	st, _ := store.Open(cfg.DataDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connectYoto(t, st, "dev")
	s, err := New(ctx, cfg, st, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	// Connect: our server starts ABS's mobile flow and sends the browser to the IdP.
	rec := do("GET", "/auth/abs/login", "")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Host != "idp.example" {
		t.Fatalf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// ABS (via mobile-redirect) sends the browser back with code+state.
	rec = do("GET", "/auth/abs/callback?code=c1&state="+loc.Query().Get("state"), "")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	acct, err := st.GetABSAccount(ctx, "dev")
	if err != nil || acct.Kind != store.ABSKindAPIKey || acct.Token != "the-key" {
		t.Fatalf("account: %+v %v", acct, err)
	}

	// Create a card from the book.
	rec = do("POST", "/api/cards", `{"sourceType":"abs_item","sourceId":"book1"}`)
	var card cardView
	json.Unmarshal(rec.Body.Bytes(), &card)
	if rec.Code != http.StatusCreated || card.Title != "Winnie" || card.TrackCount != 3 || card.LastError != "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	tracks, _ := st.ListTracks(ctx, card.ID)
	if tracks[0].Mode != store.ModeRaw || tracks[1].Mode != store.ModeCut || tracks[1].Start != 0 || tracks[1].End != 80 ||
		tracks[2].Start != 80 || tracks[2].End != 200 {
		t.Fatalf("tracks: %+v", tracks)
	}
	if got := fy.content[0].Content.Chapters[2].Title; got != "Ch 3" {
		t.Errorf("chapter title %q", got)
	}
	full, _ := st.GetCard(ctx, card.ID)

	// Whole mp3 file streams straight from ABS with Range.
	req := httptest.NewRequest("GET", "/s/"+full.StreamToken+"/01.mp3", nil)
	req.Header.Set("Range", "bytes=0-4")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(mp3Bytes[:5]) {
		t.Errorf("raw: %d %q", rec.Code, rec.Body.String())
	}

	// Chapter cuts: wait for the background cache, then serve with Range.
	deadline := time.Now().Add(5 * time.Second)
	for {
		p := s.cache.Progress(ctx, full)
		if p != nil && p.Done == 2 && !p.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache did not finish: %+v", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
	rec = do("GET", "/s/"+full.StreamToken+"/03.mp3", "")
	if rec.Code != 200 || rec.Body.String() != "cut:80.000:120.000" || rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("cached cut: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}

	// A cut that is not cached yet is rendered live.
	os.Remove(s.cache.path(full, &tracks[1]))
	rec = do("GET", "/s/"+full.StreamToken+"/02.mp3", "")
	if rec.Code != 200 || rec.Body.String() != "cut:0.000:80.000" {
		t.Errorf("live cut: %d %q", rec.Code, rec.Body.String())
	}

	// Deleting the last card for a book removes its cache.
	if rec = do("DELETE", "/api/cards/1?keepYoto=1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "cache", "abs", "book1")); !os.IsNotExist(err) {
		t.Errorf("cache dir not removed: %v", err)
	}
}
