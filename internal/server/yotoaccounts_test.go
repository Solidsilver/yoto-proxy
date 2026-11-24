package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// connectYoto gives login user a Yoto account holding the token the fake
// Yoto API expects, and returns its id.
func connectYoto(t *testing.T, st *store.Store, user string) int64 {
	t.Helper()
	tok, _ := json.Marshal(yoto.Tokens{AccessToken: "access-1", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	ctx := context.Background()
	id, err := st.CreateYotoAccount(ctx, "", user+"’s Yoto", string(tok), user)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMember(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func fakeJWT(sub string) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]any{"sub": sub, "exp": time.Now().Add(time.Hour).Unix()})
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

// asUser returns a request carrying a signed session for user.
func asUser(s *Server, user, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.sign(session{
		Sub: user, Name: user, User: user, Exp: time.Now().Add(time.Hour).Unix()})})
	return r
}

func TestYotoAccountsAreSeparateAndShareable(t *testing.T) {
	nav := fakeNavidrome(t, "")
	defer nav.Close()
	fy := newFakeYoto(t)
	defer fy.srv.Close()
	yoto.APIBase = fy.srv.URL

	cfg := &config.Config{
		PublicURL: "https://yoto.example", NavidromeURL: nav.URL, NavidromeAuth: "password",
		NavidromeUser: "yoto", NavidromePassword: "pw", TranscodeBitrate: 192, YotoClientID: "cid",
		OIDCIssuer: "https://auth.example", DataDir: t.TempDir(),
	}
	st, _ := store.Open(cfg.DataDir)
	connectYoto(t, st, "luke")
	s, err := New(context.Background(), cfg, st, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	do := func(user, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asUser(s, user, method, path, body))
		return rec
	}

	if rec := do("luke", "POST", "/api/cards", `{"sourceType":"navidrome_playlist","sourceId":"pl1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}

	// Another user without a Yoto account sees nothing and cannot touch the card.
	if rec := do("wife", "GET", "/api/cards", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("wife sees cards: %s", rec.Body)
	}
	for _, req := range [][2]string{{"GET", "/api/cards/1"}, {"POST", "/api/cards/1/sync"}, {"DELETE", "/api/cards/1"}, {"GET", "/api/cards/1/cover"}} {
		if rec := do("wife", req[0], req[1], ""); rec.Code != http.StatusNotFound {
			t.Errorf("wife %s %s: want 404, got %d", req[0], req[1], rec.Code)
		}
	}
	if rec := do("wife", "POST", "/api/cards", `{"sourceType":"navidrome_playlist","sourceId":"pl1","title":"x"}`); rec.Code != http.StatusConflict {
		t.Errorf("create without Yoto: want 409, got %d", rec.Code)
	}

	// Luke invites; the invite works once.
	rec := do("luke", "POST", "/api/yoto/invites", "")
	var inv struct{ URL string }
	json.Unmarshal(rec.Body.Bytes(), &inv)
	token := inv.URL[strings.LastIndex(inv.URL, "/")+1:]
	if rec.Code != http.StatusCreated || !strings.HasPrefix(inv.URL, "https://yoto.example/invite/") {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body)
	}
	if rec := do("wife", "GET", "/api/invites/"+token, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"invitedBy":"luke"`) {
		t.Errorf("get invite: %d %s", rec.Code, rec.Body)
	}
	if rec := do("wife", "POST", "/api/invites/"+token+"/accept", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	if rec := do("someone", "POST", "/api/invites/"+token+"/accept", ""); rec.Code != http.StatusNotFound {
		t.Errorf("invite reused: %d", rec.Code)
	}

	// Now she shares the account and its cards.
	var cards []cardView
	json.Unmarshal(do("wife", "GET", "/api/cards", "").Body.Bytes(), &cards)
	if len(cards) != 1 || cards[0].Owner != "luke" {
		t.Fatalf("wife cards after joining: %+v", cards)
	}
	if rec := do("wife", "POST", "/api/cards/1/sync", ""); rec.Code != 200 {
		t.Errorf("wife sync: %d %s", rec.Code, rec.Body)
	}

	// Leaving: luke leaves, the account keeps its tokens for the wife.
	do("luke", "POST", "/api/yoto/leave", "")
	if rec := do("luke", "GET", "/api/cards", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("luke still sees cards after leaving")
	}
	if rec := do("wife", "POST", "/api/cards/1/sync", ""); rec.Code != 200 {
		t.Errorf("wife sync after luke left: %d %s", rec.Code, rec.Body)
	}
}

func TestLegacyYotoConnectionMigrates(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(t.TempDir())
	tok, _ := json.Marshal(yoto.Tokens{AccessToken: fakeJWT("yoto|abc"), RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	st.SetSetting(ctx, "yoto_tokens", string(tok))
	c1, _ := st.CreateCard(ctx, 0, "Kind of Blue", store.SourceNavidromeAlbum, "al1", "solidsilver")
	st.CreateCard(ctx, 0, "Old", store.SourceNavidromeAlbum, "al2", "")

	cfg := &config.Config{PublicURL: "https://yoto.example", NavidromeAuth: "header", YotoClientID: "cid", AuthDisabled: true, DataDir: t.TempDir()}
	s, err := New(ctx, cfg, st, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.MemberAccount(ctx, "solidsilver")
	if err != nil {
		t.Fatalf("owner not made a member: %v", err)
	}
	acct, _ := st.GetYotoAccount(ctx, id)
	if acct.Subject != "yoto|abc" || acct.Tokens == "" {
		t.Errorf("account: %+v", acct)
	}
	cards, _ := st.ListCards(ctx, id)
	if len(cards) != 2 || cards[0].ID != c1.ID && cards[1].ID != c1.ID {
		t.Errorf("cards not moved: %+v", cards)
	}
	if _, err := st.GetSetting(ctx, "yoto_tokens"); err != store.ErrNotFound {
		t.Errorf("legacy setting kept")
	}
	// Restarting does not migrate again.
	if _, err := New(ctx, cfg, st, fstest.MapFS{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountYotoAccounts(ctx); n != 1 {
		t.Errorf("accounts after restart: %d", n)
	}
	_ = s
}

// Two users who connect the same Yoto account end up sharing it; a different
// Yoto account gets its own group.
func TestConnectingSameYotoAccountJoinsIt(t *testing.T) {
	subs := map[string]string{"code-fam": "yoto|family", "code-other": "yoto|other"}
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": fakeJWT(subs[r.Form.Get("code")]), "refresh_token": "r", "expires_in": 3600,
		})
	}))
	defer login.Close()
	yoto.AuthBase = login.URL
	defer func() { yoto.AuthBase = "https://login.yotoplay.com" }()

	cfg := &config.Config{PublicURL: "https://yoto.example", NavidromeAuth: "header", YotoClientID: "cid",
		OIDCIssuer: "https://auth.example", DataDir: t.TempDir()}
	st, _ := store.Open(cfg.DataDir)
	s, _ := New(context.Background(), cfg, st, fstest.MapFS{})
	h := s.Handler()
	connect := func(user, code string) int64 {
		// Start the flow to get a signed flow cookie, then hit the callback with it.
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, asUser(s, user, "GET", "/auth/yoto/login", ""))
		loc := rec.Header().Get("Location")
		state := loc[strings.Index(loc, "state=")+6:]
		state = state[:strings.IndexByte(state+"&", '&')]
		req := asUser(s, user, "GET", "/auth/yoto/callback?code="+code+"&state="+state, "")
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("callback for %s: %d %s", user, rec.Code, rec.Body)
		}
		id, err := st.MemberAccount(context.Background(), user)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c := connect("luke", "code-fam"), connect("wife", "code-fam"), connect("grandma", "code-other")
	if a != b || a == c {
		t.Errorf("accounts: luke=%d wife=%d grandma=%d", a, b, c)
	}
}
