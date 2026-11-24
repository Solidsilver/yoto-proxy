// Package abs is a small Audiobookshelf client: OpenID login via the
// server's mobile-app flow, API keys and token refresh, and the library
// browsing/streaming endpoints yoto-proxy needs.
package abs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	internal string // API base, e.g. http://127.0.0.1:13378
	public   string // browser-facing base, e.g. https://shelf.example.com (login flow only)
	http     *http.Client
	media    *http.Client
}

func New(internalURL, publicURL string) *Client {
	return &Client{
		internal: strings.TrimRight(internalURL, "/"),
		public:   strings.TrimRight(publicURL, "/"),
		http:     &http.Client{Timeout: 30 * time.Second},
		media:    &http.Client{},
	}
}

// InternalURL is the API base (used to build ffmpeg input URLs).
func (c *Client) InternalURL() string { return c.internal }

// --- OpenID login (Audiobookshelf's mobile-app flow) ---
//
//  1. StartLogin: we call {public}/auth/openid with our redirect_uri and a
//     PKCE challenge. Audiobookshelf answers with a redirect to the identity
//     provider and sets session cookies, which we keep server-side.
//  2. The browser follows that redirect, logs in (usually silently), comes
//     back to Audiobookshelf's /auth/openid/mobile-redirect, and is sent on
//     to our redirect_uri with ?code&state.
//  3. FinishLogin: we call /auth/openid/callback with the code, state, PKCE
//     verifier and the saved cookies and receive the user and tokens.
//
// redirect_uri must be listed in Audiobookshelf's "Allowed Mobile Redirect URIs".

type LoginFlow struct {
	State    string
	Verifier string
	Cookies  []*http.Cookie
}

// StartLogin returns the identity-provider URL to send the browser to.
func (c *Client) StartLogin(ctx context.Context, redirectURI, state, verifier string) (string, *LoginFlow, error) {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	u := c.public + "/auth/openid?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("audiobookshelf login: %w", err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || loc == "" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", nil, fmt.Errorf("audiobookshelf login: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	pu, _ := url.Parse(c.public)
	return loc, &LoginFlow{State: state, Verifier: verifier, Cookies: jar.Cookies(pu)}, nil
}

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Type         string `json:"type"` // root, admin, user, guest
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func (u *User) IsAdmin() bool { return u.Type == "root" || u.Type == "admin" }

// FinishLogin exchanges the code for the user's tokens.
func (c *Client) FinishLogin(ctx context.Context, f *LoginFlow, code string) (*User, error) {
	q := url.Values{"code": {code}, "state": {f.State}, "code_verifier": {f.Verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.public+"/auth/openid/callback?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	for _, ck := range f.Cookies {
		req.AddCookie(ck)
	}
	var out struct {
		User User `json:"user"`
	}
	if err := c.doJSON(c.http, req, &out); err != nil {
		return nil, fmt.Errorf("audiobookshelf login: %w", err)
	}
	if out.User.ID == "" || out.User.AccessToken == "" {
		return nil, errors.New("audiobookshelf login: response had no user or token")
	}
	return &out.User, nil
}

// CreateAPIKey creates a non-expiring API key acting as userID. The caller's
// token must belong to an admin.
func (c *Client) CreateAPIKey(ctx context.Context, token, userID, name string) (string, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "userId": userID, "isActive": true})
	req, err := c.apiRequest(ctx, http.MethodPost, "/api/api-keys", token, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		APIKey struct {
			APIKey string `json:"apiKey"`
		} `json:"apiKey"`
	}
	if err := c.doJSON(c.http, req, &out); err != nil {
		return "", fmt.Errorf("create API key: %w", err)
	}
	if out.APIKey.APIKey == "" {
		return "", errors.New("create API key: no key in response")
	}
	return out.APIKey.APIKey, nil
}

// Refresh trades a refresh token for new tokens. Audiobookshelf rotates
// refresh tokens, so the returned one must replace the old.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.internal+"/auth/refresh", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-refresh-token", refreshToken)
	var out struct {
		User User `json:"user"`
	}
	if err := c.doJSON(c.http, req, &out); err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	if out.User.RefreshToken == "" {
		out.User.RefreshToken = refreshToken
	}
	return &out.User, nil
}

// --- library API ---

type Library struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"` // "book" or "podcast"
}

type Metadata struct {
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle"`
	AuthorName string `json:"authorName"`
	SeriesName string `json:"seriesName"`
}

type FileMetadata struct {
	Filename string `json:"filename"`
	Ext      string `json:"ext"`
	Size     int64  `json:"size"`
}

type AudioFile struct {
	Index    int          `json:"index"`
	Ino      string       `json:"ino"`
	Duration float64      `json:"duration"`
	Codec    string       `json:"codec"`
	MimeType string       `json:"mimeType"`
	Exclude  bool         `json:"exclude"`
	Metadata FileMetadata `json:"metadata"`
}

type Chapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

type Media struct {
	Metadata      Metadata    `json:"metadata"`
	Duration      float64     `json:"duration"`
	NumAudioFiles int         `json:"numAudioFiles"`
	NumChapters   int         `json:"numChapters"`
	AudioFiles    []AudioFile `json:"audioFiles,omitempty"`
	Chapters      []Chapter   `json:"chapters,omitempty"`
}

type Item struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	MediaType string `json:"mediaType"`
	Media     Media  `json:"media"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (c *Client) Libraries(ctx context.Context, token string) ([]Library, error) {
	var out struct {
		Libraries []Library `json:"libraries"`
	}
	if err := c.getJSON(ctx, token, "/api/libraries", &out); err != nil {
		return nil, err
	}
	return nonNil(out.Libraries), nil
}

// Items lists a library's items (minified), or searches when query is set.
func (c *Client) Items(ctx context.Context, token, libraryID, query string, page, limit int) ([]Item, int, error) {
	lib := "/api/libraries/" + url.PathEscape(libraryID)
	if query != "" {
		var out struct {
			Book []struct {
				LibraryItem Item `json:"libraryItem"`
			} `json:"book"`
		}
		q := url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}
		if err := c.getJSON(ctx, token, lib+"/search?"+q.Encode(), &out); err != nil {
			return nil, 0, err
		}
		items := make([]Item, 0, len(out.Book))
		for _, b := range out.Book {
			items = append(items, b.LibraryItem)
		}
		return items, len(items), nil
	}
	q := url.Values{
		"limit": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)},
		"sort": {"media.metadata.title"}, "minified": {"1"},
	}
	var out struct {
		Results []Item `json:"results"`
		Total   int    `json:"total"`
	}
	if err := c.getJSON(ctx, token, lib+"/items?"+q.Encode(), &out); err != nil {
		return nil, 0, err
	}
	return nonNil(out.Results), out.Total, nil
}

// Item returns an expanded item, including audio files and chapters.
func (c *Client) Item(ctx context.Context, token, id string) (*Item, error) {
	var it Item
	if err := c.getJSON(ctx, token, "/api/items/"+url.PathEscape(id)+"?expanded=1", &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// Cover opens an item's cover image. The caller must close the body.
func (c *Client) Cover(ctx context.Context, token, id string, width int) (*http.Response, error) {
	path := "/api/items/" + url.PathEscape(id) + "/cover?format=jpeg"
	if width > 0 {
		path += "&width=" + strconv.Itoa(width)
	}
	req, err := c.apiRequest(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("audiobookshelf cover: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// FileURL is the URL of one of an item's audio files (Range-capable).
func (c *Client) FileURL(itemID, ino string) string {
	return c.internal + "/api/items/" + url.PathEscape(itemID) + "/file/" + url.PathEscape(ino)
}

// File opens an audio file, forwarding Range. The caller must close the body.
func (c *Client) File(ctx context.Context, token, itemID, ino, rangeHdr, ifRange string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.FileURL(itemID, ino), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}
	}
	return c.media.Do(req)
}

// --- helpers ---

// ErrUnauthorized means the token was rejected.
var ErrUnauthorized = errors.New("audiobookshelf: unauthorized")

func (c *Client) apiRequest(ctx context.Context, method, path, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.internal+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

func (c *Client) getJSON(ctx context.Context, token, path string, out any) error {
	req, err := c.apiRequest(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return err
	}
	return c.doJSON(c.http, req, out)
}

func (c *Client) doJSON(hc *http.Client, req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, req.URL.Path, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", req.URL.Path, err)
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
