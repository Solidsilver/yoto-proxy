package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Solidsilver/yoto-proxy/internal/config"
	"github.com/Solidsilver/yoto-proxy/internal/store"
	"github.com/Solidsilver/yoto-proxy/internal/yoto"
)

var mp3Bytes = bytes.Repeat([]byte("ID3fakeaudio"), 100)

func pngBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// fakeNavidrome serves a two-song playlist: one mp3 (raw, range-capable)
// and one flac (transcoded). With headerUser set it expects ExtAuth requests
// for that user; otherwise Subsonic token auth for "yoto".
func fakeNavidrome(t *testing.T, headerUser string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if headerUser != "" {
			if got := r.Header.Get("Remote-User"); got != headerUser || q.Get("t") != "" {
				t.Errorf("want ExtAuth as %q, got header %q params %v", headerUser, got, q)
			}
		} else if q.Get("u") != "yoto" || q.Get("t") == "" || q.Get("s") == "" {
			t.Errorf("missing subsonic auth: %v", q)
		}
		ok := func(body string) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"subsonic-response":{"status":"ok"`+body+`}}`)
		}
		switch r.URL.Path {
		case "/rest/ping":
			ok("")
		case "/rest/getPlaylist":
			ok(`,"playlist":{"id":"pl1","name":"Bedtime","coverArt":"pl-pl1","entry":[
				{"id":"s1","title":"Lullaby","artist":"Mum","duration":120,"suffix":"mp3"},
				{"id":"s2","title":"Night Song","artist":"Dad","duration":200,"suffix":"flac"}]}`)
		case "/rest/getCoverArt":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngBytes())
		case "/rest/stream":
			if q.Get("format") == "raw" {
				http.ServeContent(w, r, "a.mp3", time.Unix(0, 0), bytes.NewReader(mp3Bytes))
				return
			}
			if q.Get("format") != "mp3" || q.Get("maxBitRate") != "192" {
				t.Errorf("unexpected transcode params: %v", q)
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("transcoded"))
		default:
			t.Errorf("unexpected navidrome call %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

type fakeYoto struct {
	srv     *httptest.Server
	content []yoto.Content
}

func newFakeYoto(t *testing.T) *fakeYoto {
	f := &fakeYoto{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-1" {
			t.Errorf("bad auth header %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/content":
			var c yoto.Content
			json.NewDecoder(r.Body).Decode(&c)
			f.content = append(f.content, c)
			id := c.CardID
			if id == "" {
				id = "abc12"
			}
			json.NewEncoder(w).Encode(map[string]any{"card": map[string]any{"cardId": id}})
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/media/coverImage/"):
			body, _ := io.ReadAll(r.Body)
			if cfg, err := jpeg.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 640 || cfg.Height != 1024 {
				t.Errorf("cover upload is not 640x1024 jpeg: %+v %v", cfg, err)
			}
			io.WriteString(w, `{"coverImage":{"mediaId":"m1","mediaUrl":"https://cdn.yoto/cover.jpg"}}`)
		default:
			t.Errorf("unexpected yoto call %s %s", r.Method, r.URL.Path)
		}
	}))
	return f
}

func TestCreateSyncAndStream(t *testing.T) {
	nav := fakeNavidrome(t, "")
	defer nav.Close()
	fy := newFakeYoto(t)
	defer fy.srv.Close()
	yoto.APIBase = fy.srv.URL

	cfg := &config.Config{
		PublicURL: "https://yoto.example", NavidromeURL: nav.URL, NavidromeAuth: "password", NavidromeUser: "yoto",
		NavidromePassword: "pw", TranscodeBitrate: 192, YotoClientID: "cid", AuthDisabled: true,
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connectYoto(t, st, "dev")

	s, err := New(ctx, cfg, st, fstest.MapFS{"index.html": {Data: []byte("<html>")}})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	// Create a card from the playlist.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/cards",
		strings.NewReader(`{"sourceType":"navidrome_playlist","sourceId":"pl1"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var card store.Card
	json.Unmarshal(rec.Body.Bytes(), &card)
	if card.YotoCardID != "abc12" || card.Title != "Bedtime" || card.TrackCount != 2 || card.LastError != "" {
		t.Fatalf("unexpected card: %+v", card)
	}

	// Check what Yoto received.
	if len(fy.content) != 1 {
		t.Fatalf("expected 1 content push, got %d", len(fy.content))
	}
	c := fy.content[0]
	if c.Metadata.Cover == nil || c.Metadata.Cover.ImageL != "https://cdn.yoto/cover.jpg" {
		t.Errorf("cover not set: %+v", c.Metadata)
	}
	if len(c.Content.Chapters) != 2 {
		t.Fatalf("chapters: %+v", c.Content.Chapters)
	}
	tr := c.Content.Chapters[1].Tracks[0]
	full, _ := st.GetCard(ctx, card.ID)
	wantURL := "https://yoto.example/s/" + full.StreamToken + "/02.mp3"
	if tr.Type != "stream" || tr.Format != "mp3" || tr.TrackURL != wantURL || tr.Duration != 200 {
		t.Errorf("unexpected track: %+v (want url %s)", tr, wantURL)
	}

	// Raw mp3 passthrough honours Range.
	req := httptest.NewRequest("GET", "/s/"+full.StreamToken+"/01.mp3", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(mp3Bytes[:10]) ||
		rec.Header().Get("Content-Range") == "" || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("range stream: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}

	// Non-mp3 is transcoded.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/s/"+full.StreamToken+"/02.mp3", nil))
	if rec.Code != 200 || rec.Body.String() != "transcoded" {
		t.Errorf("transcode stream: %d %q", rec.Code, rec.Body.String())
	}

	// Bad token / unknown track are 404.
	for _, p := range []string{"/s/nope/01.mp3", "/s/" + full.StreamToken + "/99.mp3"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 404 {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}

	// Re-sync updates the same Yoto card, and rotating the token kills old URLs.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/cards/1/rotate-token", nil))
	if rec.Code != 200 || fy.content[1].CardID != "abc12" {
		t.Fatalf("rotate: %d, cardId %q", rec.Code, fy.content[len(fy.content)-1].CardID)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/s/"+full.StreamToken+"/01.mp3", nil))
	if rec.Code != 404 {
		t.Errorf("old token still works: %d", rec.Code)
	}
	if !strings.Contains(fy.content[1].Content.Chapters[0].Tracks[0].TrackURL, "/s/") ||
		strings.Contains(fy.content[1].Content.Chapters[0].Tracks[0].TrackURL, full.StreamToken) {
		t.Errorf("re-sync did not use the new token")
	}
}

func TestAPIRequiresSession(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	cfg := &config.Config{PublicURL: "https://yoto.example", OIDCIssuer: "https://auth.example"}
	s, _ := New(context.Background(), cfg, st, fstest.MapFS{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/cards", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
		t.Errorf("want redirect to login, got %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Icons and the manifest load without a session (iOS home screen, favicons).
	s.static = fstest.MapFS{"apple-touch-icon.png": {Data: []byte("png")}, "manifest.webmanifest": {Data: []byte("{}")}}
	for path, ct := range map[string]string{"/apple-touch-icon.png": "image/png", "/manifest.webmanifest": "application/manifest+json"} {
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

// In header mode, requests act as the logged-in user (mapped through
// NavidromeUserMap), cards record that owner, and streams act as the owner.
func TestHeaderAuthActsAsOwner(t *testing.T) {
	nav := fakeNavidrome(t, "luke")
	defer nav.Close()
	fy := newFakeYoto(t)
	defer fy.srv.Close()
	yoto.APIBase = fy.srv.URL

	cfg := &config.Config{
		PublicURL: "https://yoto.example", NavidromeURL: nav.URL, NavidromeAuth: "header",
		NavidromeHeader: "Remote-User", NavidromeUserMap: map[string]string{"dev": "luke"},
		TranscodeBitrate: 192, YotoClientID: "cid", AuthDisabled: true, // dev session user is "dev"
	}
	st, _ := store.Open(t.TempDir())
	ctx := context.Background()
	connectYoto(t, st, "dev")
	s, err := New(ctx, cfg, st, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/cards",
		strings.NewReader(`{"sourceType":"navidrome_playlist","sourceId":"pl1"}`)))
	var card store.Card
	json.Unmarshal(rec.Body.Bytes(), &card)
	if rec.Code != http.StatusCreated || card.Owner != "dev" || card.LastError != "" {
		t.Fatalf("create: %d %+v", rec.Code, card)
	}
	full, _ := st.GetCard(ctx, card.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/s/"+full.StreamToken+"/02.mp3", nil))
	if rec.Code != 200 {
		t.Errorf("stream as owner: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/cards/1/cover", nil))
	if c, err := jpeg.DecodeConfig(rec.Body); rec.Code != 200 || err != nil || c.Height != 1024 {
		t.Errorf("card cover: %d %v", rec.Code, err)
	}
}
