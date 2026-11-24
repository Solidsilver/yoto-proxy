package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Solidsilver/yoto-proxy/internal/store"
)

// chapterCache renders ModeCut tracks (a time range of an Audiobookshelf
// file) to mp3 files on disk, so they can be served with Content-Length and
// Range. A single background worker processes cards one at a time.
type chapterCache struct {
	s     *Server
	dir   string
	queue chan int64

	mu       sync.Mutex
	queued   map[int64]bool
	progress map[int64]*cacheProgress
}

type cacheProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

func newChapterCache(s *Server, dir string) *chapterCache {
	return &chapterCache{
		s: s, dir: dir,
		queue:    make(chan int64, 256),
		queued:   map[int64]bool{},
		progress: map[int64]*cacheProgress{},
	}
}

func (c *chapterCache) Enqueue(cardID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued[cardID] {
		return
	}
	select {
	case c.queue <- cardID:
		c.queued[cardID] = true
	default:
		slog.Warn("chapter cache queue full", "card", cardID)
	}
}

// Progress reports cache status for a card's cut tracks (nil if it has none).
func (c *chapterCache) Progress(ctx context.Context, card *store.Card) *cacheProgress {
	c.mu.Lock()
	var p *cacheProgress
	if cur := c.progress[card.ID]; cur != nil {
		cp := *cur // copy under the lock; the worker updates it
		p = &cp
	}
	c.mu.Unlock()
	if p != nil && p.Running {
		return p
	}
	tracks, err := c.s.store.ListTracks(ctx, card.ID)
	if err != nil {
		return nil
	}
	out := &cacheProgress{}
	for _, t := range tracks {
		if t.Mode != store.ModeCut {
			continue
		}
		out.Total++
		if _, err := os.Stat(c.path(card, &t)); err == nil {
			out.Done++
		}
	}
	if out.Total == 0 {
		return nil
	}
	if p != nil {
		out.Error = p.Error
	}
	c.mu.Lock()
	out.Running = c.queued[card.ID]
	c.mu.Unlock()
	return out
}

// Run processes queued cards until ctx is done.
func (c *chapterCache) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-c.queue:
			c.fill(ctx, id)
			c.mu.Lock()
			delete(c.queued, id)
			c.mu.Unlock()
		}
	}
}

func (c *chapterCache) fill(ctx context.Context, cardID int64) {
	card, err := c.s.store.GetCard(ctx, cardID)
	if err != nil {
		return
	}
	tracks, err := c.s.store.ListTracks(ctx, cardID)
	if err != nil {
		return
	}
	var todo []store.Track
	total := 0
	for _, t := range tracks {
		if t.Mode != store.ModeCut {
			continue
		}
		total++
		if _, err := os.Stat(c.path(card, &t)); err != nil {
			todo = append(todo, t)
		}
	}
	p := &cacheProgress{Total: total, Done: total - len(todo), Running: true}
	c.mu.Lock()
	c.progress[cardID] = p
	c.mu.Unlock()

	for i := range todo {
		if ctx.Err() != nil {
			return
		}
		if err := c.render(ctx, card, &todo[i]); err != nil {
			slog.Error("chapter cache", "card", cardID, "track", todo[i].Key, "err", err)
			c.mu.Lock()
			p.Error = err.Error()
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		p.Done++
		c.mu.Unlock()
	}
	c.mu.Lock()
	p.Running = false
	c.mu.Unlock()
	slog.Info("chapter cache done", "card", cardID, "done", p.Done, "total", p.Total)
}

// path is the cache file for a cut track. It depends only on the source
// range and encoding, so cards sharing a book share files.
func (c *chapterCache) path(card *store.Card, t *store.Track) string {
	name := fmt.Sprintf("%s_%.3f_%.3f_%dk.mp3", t.SourceTrackID, t.Start, t.End, c.s.cfg.ABSBitrate)
	return filepath.Join(c.dir, "abs", safeName(card.SourceID), safeName(name))
}

func (c *chapterCache) render(ctx context.Context, card *store.Card, t *store.Track) error {
	dst := c.path(card, t)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".part-*.mp3")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	args, err := c.ffmpegArgs(ctx, card, t)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.s.cfg.FFmpegPath, append(args, "-y", tmp.Name())...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLine(string(out)))
	}
	return os.Rename(tmp.Name(), dst)
}

// ffmpegArgs builds the input/encode arguments (without the output) for a
// cut track. ffmpeg reads the file over HTTP from Audiobookshelf, seeking
// with Range requests.
func (c *chapterCache) ffmpegArgs(ctx context.Context, card *store.Card, t *store.Track) ([]string, error) {
	if c.s.abs == nil {
		return nil, errABSDisabled
	}
	token, err := c.s.absToken(ctx, card.Owner)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-headers", "Authorization: Bearer " + token + "\r\n",
		"-ss", strconv.FormatFloat(t.Start, 'f', 3, 64),
		"-i", c.s.abs.FileURL(card.SourceID, t.SourceTrackID),
		"-t", strconv.FormatFloat(t.End-t.Start, 'f', 3, 64),
		"-map", "0:a:0", "-vn", "-map_metadata", "-1",
	}
	if strings.EqualFold(t.Suffix, "mp3") {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-ac", "1", "-c:a", "libmp3lame", "-b:a", strconv.Itoa(c.s.cfg.ABSBitrate)+"k")
	}
	return append(args, "-f", "mp3"), nil
}

// streamLive renders a cut track straight to w while it is not cached yet.
// No Content-Length or Range is possible here.
func (c *chapterCache) streamLive(ctx context.Context, w io.Writer, card *store.Card, t *store.Track) error {
	args, err := c.ffmpegArgs(ctx, card, t)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.s.cfg.FFmpegPath, append(args, "pipe:1")...)
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLine(stderr.String()))
	}
	return nil
}

// RemoveSource deletes cached files for a source no card uses any more.
func (c *chapterCache) RemoveSource(ctx context.Context, sourceType, sourceID string) {
	if sourceType != store.SourceABSItem {
		return
	}
	if n, err := c.s.store.CountCardsForSource(ctx, sourceType, sourceID); err != nil || n > 0 {
		return
	}
	dir := filepath.Join(c.dir, "abs", safeName(sourceID))
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("remove cache", "dir", dir, "err", err)
	}
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r == ':' {
			return '_'
		}
		return r
	}, strings.TrimLeft(s, "."))
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
