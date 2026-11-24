// Package yoto talks to the Yoto developer API (https://yoto.dev): OAuth with
// PKCE, creating/updating "Make Your Own" playlist content, and cover uploads.
package yoto

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
	"net/url"
	"strings"
	"sync"
	"time"
)

// Overridable in tests.
var (
	AuthBase = "https://login.yotoplay.com"
	APIBase  = "https://api.yotoplay.com"
)

const (
	audience = "https://api.yotoplay.com"
	scopes   = "user:content:view user:content:manage user:icons:manage offline_access"

	// DefaultIcon is Yoto's stock "Make Your Own" 16x16 icon.
	DefaultIcon = "yoto:#aUm9i3ex3qqAMYBv-i-O-pYMKuMJGICtR3Vhf289u2Q"
)

var ErrNotConnected = errors.New("yoto account not connected")

type Tokens struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	Expiry       time.Time `json:"expiry"`
}

// TokenStore persists tokens. Yoto refresh tokens are single-use, so every
// refresh must be saved before the old token is forgotten.
type TokenStore interface {
	LoadTokens(ctx context.Context) (*Tokens, error) // nil, nil when not connected
	SaveTokens(ctx context.Context, t *Tokens) error
}

// Client holds the app registration and performs the OAuth flow. API calls
// are made through an Account, which owns one Yoto account's tokens.
type Client struct {
	clientID, clientSecret, redirectURI string
	http                                *http.Client
}

func New(clientID, clientSecret, redirectURI string) *Client {
	return &Client{
		clientID: clientID, clientSecret: clientSecret, redirectURI: redirectURI,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Account makes API calls for one Yoto account. Keep one Account per token
// store: it serializes refreshes, which matters because refresh tokens are
// single-use.
type Account struct {
	c     *Client
	store TokenStore
	mu    sync.Mutex
}

func (c *Client) Account(store TokenStore) *Account { return &Account{c: c, store: store} }

// Subject returns the Yoto user id (the access token's "sub" claim), which
// identifies the Yoto account. Empty if the token is not a readable JWT.
func Subject(t *Tokens) string {
	parts := strings.Split(t.AccessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Sub
}

// --- OAuth ---

// AuthURL returns the login URL. verifier is a random PKCE code verifier the
// caller must keep until the callback.
func (c *Client) AuthURL(state, verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.clientID},
		"redirect_uri":          {c.redirectURI},
		"scope":                 {scopes},
		"audience":              {audience},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return AuthBase + "/authorize?" + q.Encode()
}

// Exchange trades an authorization code for tokens.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (*Tokens, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {c.redirectURI},
	}
	return c.tokenRequest(ctx, form)
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (*Tokens, error) {
	if c.clientSecret != "" {
		form.Set("client_secret", c.clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthBase+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yoto token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yoto token: HTTP %d: %s", resp.StatusCode, truncate(body))
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("yoto token: decode: %w", err)
	}
	if tr.ExpiresIn == 0 {
		tr.ExpiresIn = 3600
	}
	return &Tokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}, nil
}

// accessToken returns a valid access token, refreshing (and persisting) if needed.
func (a *Account) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.store.LoadTokens(ctx)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", ErrNotConnected
	}
	if time.Until(t.Expiry) > 2*time.Minute {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", ErrNotConnected
	}
	nt, err := a.c.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {a.c.clientID},
		"refresh_token": {t.RefreshToken},
	})
	if err != nil {
		return "", fmt.Errorf("refresh: %w", err)
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = t.RefreshToken
	}
	if err := a.store.SaveTokens(ctx, nt); err != nil {
		return "", err
	}
	return nt.AccessToken, nil
}

// Connected reports whether we hold usable tokens (refreshing if needed).
func (a *Account) Connected(ctx context.Context) bool {
	_, err := a.accessToken(ctx)
	return err == nil
}

// --- content ---

type Display struct {
	Icon16x16 string `json:"icon16x16"`
}

type Track struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	TrackURL     string   `json:"trackUrl"`
	Type         string   `json:"type"`   // "stream" or "audio"
	Format       string   `json:"format"` // "mp3", "aac", ...
	Duration     int      `json:"duration,omitempty"`
	OverlayLabel string   `json:"overlayLabel,omitempty"`
	Display      *Display `json:"display,omitempty"`
}

type Chapter struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	OverlayLabel string   `json:"overlayLabel,omitempty"`
	Duration     int      `json:"duration,omitempty"`
	Tracks       []Track  `json:"tracks"`
	Display      *Display `json:"display,omitempty"`
}

type Cover struct {
	ImageL string `json:"imageL"`
}

type Metadata struct {
	Description string `json:"description,omitempty"`
	Cover       *Cover `json:"cover,omitempty"`
}

type Body struct {
	Chapters []Chapter `json:"chapters"`
}

type Content struct {
	CardID   string   `json:"cardId,omitempty"`
	Title    string   `json:"title"`
	Content  Body     `json:"content"`
	Metadata Metadata `json:"metadata"`
}

// SaveContent creates content (CardID empty) or overwrites it (CardID set)
// and returns the card id.
func (a *Account) SaveContent(ctx context.Context, content *Content) (string, error) {
	payload, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	var out struct {
		Card struct {
			CardID string `json:"cardId"`
		} `json:"card"`
	}
	if err := a.do(ctx, http.MethodPost, APIBase+"/content", "application/json", bytes.NewReader(payload), &out); err != nil {
		return "", err
	}
	if out.Card.CardID == "" {
		return "", errors.New("yoto: response did not include a cardId")
	}
	return out.Card.CardID, nil
}

func (a *Account) DeleteContent(ctx context.Context, cardID string) error {
	return a.do(ctx, http.MethodDelete, APIBase+"/content/"+url.PathEscape(cardID), "", nil, nil)
}

// UploadCover uploads an image and returns the URL to use as metadata.cover.imageL.
func (a *Account) UploadCover(ctx context.Context, img io.Reader, contentType string) (string, error) {
	var out struct {
		CoverImage struct {
			MediaURL string `json:"mediaUrl"`
		} `json:"coverImage"`
	}
	u := APIBase + "/media/coverImage/user/me/upload?autoconvert=true&coverType=default"
	if err := a.do(ctx, http.MethodPost, u, contentType, img, &out); err != nil {
		return "", err
	}
	if out.CoverImage.MediaURL == "" {
		return "", errors.New("yoto: cover upload returned no mediaUrl")
	}
	return out.CoverImage.MediaURL, nil
}

func (a *Account) do(ctx context.Context, method, u, contentType string, body io.Reader, out any) error {
	tok, err := a.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.c.http.Do(req)
	if err != nil {
		return fmt.Errorf("yoto %s %s: %w", method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("yoto %s %s: HTTP %d: %s", method, req.URL.Path, resp.StatusCode, truncate(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("yoto %s %s: decode: %w", method, req.URL.Path, err)
		}
	}
	return nil
}

func truncate(b []byte) string {
	const max = 500
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
