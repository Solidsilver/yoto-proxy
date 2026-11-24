// Package navidrome is a small client for the Subsonic API that Navidrome serves.
package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Solidsilver/yoto-proxy/internal/store"
)

const apiVersion = "1.16.1"

// Options configures how the client authenticates.
//
// Password mode logs in as one fixed user (User/Password, Subsonic token auth).
//
// Header mode uses Navidrome's external auth: each request carries
// UserHeader: <username> and Navidrome trusts it because the request comes
// from SourceAddr, which must be listed in ExtAuth.TrustedSources. Use an
// address nothing else connects from (e.g. 127.0.0.2) so headers forwarded
// by a reverse proxy on 127.0.0.1 are never trusted.
type Options struct {
	BaseURL    string
	User       string
	Password   string
	HeaderAuth bool
	UserHeader string // default "Remote-User"
	SourceAddr string // local IP to connect from, e.g. "127.0.0.2"
}

var ErrNoUser = errors.New("navidrome: no user to act as")

type Client struct {
	opts  Options
	actAs string       // header mode: the Navidrome username for this view
	api   *http.Client // JSON calls, with timeout
	media *http.Client // streams, no overall timeout
}

func New(o Options) (*Client, error) {
	if o.UserHeader == "" {
		o.UserHeader = "Remote-User"
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if o.SourceAddr != "" {
		ip := net.ParseIP(o.SourceAddr)
		if ip == nil {
			return nil, fmt.Errorf("navidrome: invalid source address %q", o.SourceAddr)
		}
		d := &net.Dialer{Timeout: 10 * time.Second, LocalAddr: &net.TCPAddr{IP: ip}}
		tr.DialContext = d.DialContext
	}
	return &Client{
		opts:  o,
		api:   &http.Client{Timeout: 20 * time.Second, Transport: tr},
		media: &http.Client{Transport: tr},
	}, nil
}

// HeaderAuth reports whether requests act as per-user identities.
func (c *Client) HeaderAuth() bool { return c.opts.HeaderAuth }

// As returns a view of the client acting as the given Navidrome user.
// In password mode the fixed user is always used and username is ignored.
func (c *Client) As(username string) *Client {
	cp := *c
	cp.actAs = username
	return &cp
}

type Song struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	Duration  int    `json:"duration"`
	Suffix    string `json:"suffix"`
	Size      int64  `json:"size"`
	CoverArt  string `json:"coverArt"`
	Track     int    `json:"track"`
	DiscNumber int   `json:"discNumber"`
}

type Playlist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	CoverArt  string `json:"coverArt"`
	Entry     []Song `json:"entry,omitempty"`
}

type Album struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Artist    string `json:"artist"`
	Year      int    `json:"year"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	CoverArt  string `json:"coverArt"`
	Song      []Song `json:"song,omitempty"`
}

type envelope struct {
	R struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Playlists  *struct{ Playlist []Playlist } `json:"playlists"`
		Playlist   *Playlist                      `json:"playlist"`
		AlbumList2 *struct{ Album []Album }       `json:"albumList2"`
		Album      *Album                         `json:"album"`
		Search3    *struct{ Album []Album }       `json:"searchResult3"`
	} `json:"subsonic-response"`
}

// newRequest builds an authenticated Subsonic request. Password mode uses
// token auth (t = md5(password + salt)), so the password is never sent.
func (c *Client) newRequest(ctx context.Context, name string, q url.Values) (*http.Request, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("v", apiVersion)
	q.Set("c", "yoto-proxy")
	q.Set("f", "json")
	if c.opts.HeaderAuth {
		if c.actAs == "" {
			return nil, ErrNoUser
		}
		q.Set("u", c.actAs)
	} else {
		salt := store.RandomToken(9)
		sum := md5.Sum([]byte(c.opts.Password + salt))
		q.Set("u", c.opts.User)
		q.Set("t", hex.EncodeToString(sum[:]))
		q.Set("s", salt)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.BaseURL+"/rest/"+name+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.opts.HeaderAuth {
		req.Header.Set(c.opts.UserHeader, c.actAs)
	}
	return req, nil
}

func (c *Client) call(ctx context.Context, name string, q url.Values) (*envelope, error) {
	req, err := c.newRequest(ctx, name, q)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, fmt.Errorf("navidrome %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("navidrome %s: HTTP %d", name, resp.StatusCode)
	}
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("navidrome %s: decode: %w", name, err)
	}
	if env.R.Status != "ok" {
		if env.R.Error != nil {
			return nil, fmt.Errorf("navidrome %s: %s (code %d)", name, env.R.Error.Message, env.R.Error.Code)
		}
		return nil, fmt.Errorf("navidrome %s: status %q", name, env.R.Status)
	}
	return &env, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.call(ctx, "ping", nil)
	return err
}

func (c *Client) Playlists(ctx context.Context) ([]Playlist, error) {
	env, err := c.call(ctx, "getPlaylists", nil)
	if err != nil {
		return nil, err
	}
	if env.R.Playlists == nil {
		return []Playlist{}, nil
	}
	return nonNil(env.R.Playlists.Playlist), nil
}

func (c *Client) Playlist(ctx context.Context, id string) (*Playlist, error) {
	env, err := c.call(ctx, "getPlaylist", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if env.R.Playlist == nil {
		return nil, fmt.Errorf("navidrome: playlist %s missing from response", id)
	}
	env.R.Playlist.Entry = nonNil(env.R.Playlist.Entry)
	return env.R.Playlist, nil
}

// Albums lists albums alphabetically, or searches them when query is non-empty.
func (c *Client) Albums(ctx context.Context, query string, offset, size int) ([]Album, error) {
	if query != "" {
		env, err := c.call(ctx, "search3", url.Values{
			"query": {query}, "albumCount": {strconv.Itoa(size)}, "albumOffset": {strconv.Itoa(offset)},
			"artistCount": {"0"}, "songCount": {"0"},
		})
		if err != nil {
			return nil, err
		}
		if env.R.Search3 == nil {
			return []Album{}, nil
		}
		return nonNil(env.R.Search3.Album), nil
	}
	env, err := c.call(ctx, "getAlbumList2", url.Values{
		"type": {"alphabeticalByName"}, "size": {strconv.Itoa(size)}, "offset": {strconv.Itoa(offset)},
	})
	if err != nil {
		return nil, err
	}
	if env.R.AlbumList2 == nil {
		return []Album{}, nil
	}
	return nonNil(env.R.AlbumList2.Album), nil
}

func (c *Client) Album(ctx context.Context, id string) (*Album, error) {
	env, err := c.call(ctx, "getAlbum", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if env.R.Album == nil {
		return nil, fmt.Errorf("navidrome: album %s missing from response", id)
	}
	env.R.Album.Song = nonNil(env.R.Album.Song)
	return env.R.Album, nil
}

// StreamOptions controls how a song is fetched.
type StreamOptions struct {
	Raw     bool   // serve the original file (supports Range); otherwise transcode to mp3
	Bitrate int    // kbps when transcoding
	Range   string // forwarded Range header (only meaningful when Raw)
	IfRange string
}

// Stream opens the audio for a song. The caller must close the response body.
func (c *Client) Stream(ctx context.Context, id string, o StreamOptions) (*http.Response, error) {
	q := url.Values{"id": {id}}
	if o.Raw {
		q.Set("format", "raw")
	} else {
		q.Set("format", "mp3")
		q.Set("maxBitRate", strconv.Itoa(o.Bitrate))
		q.Set("estimateContentLength", "true")
	}
	req, err := c.newRequest(ctx, "stream", q)
	if err != nil {
		return nil, err
	}
	if o.Raw && o.Range != "" {
		req.Header.Set("Range", o.Range)
		if o.IfRange != "" {
			req.Header.Set("If-Range", o.IfRange)
		}
	}
	return c.media.Do(req)
}

// CoverArt opens the cover image for a coverArt id. The caller must close the body.
func (c *Client) CoverArt(ctx context.Context, id string, size int) (*http.Response, error) {
	q := url.Values{"id": {id}}
	if size > 0 {
		q.Set("size", strconv.Itoa(size))
	}
	req, err := c.newRequest(ctx, "getCoverArt", q)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("navidrome getCoverArt: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
