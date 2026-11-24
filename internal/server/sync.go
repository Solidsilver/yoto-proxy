package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/Solidsilver/yoto-proxy/internal/abs"
	"github.com/Solidsilver/yoto-proxy/internal/cover"
	"github.com/Solidsilver/yoto-proxy/internal/store"
	"github.com/Solidsilver/yoto-proxy/internal/yoto"
)

// sourceInfo is what a card's source currently looks like upstream.
type sourceInfo struct {
	Title       string
	Description string
	CoverID     string // stored as card.CoverArtID; opened with openCover
	CoverRev    string // changes when the cover image changes
	Tracks      []store.Track
}

func isNavidrome(sourceType string) bool {
	return sourceType == store.SourceNavidromePlaylist || sourceType == store.SourceNavidromeAlbum
}

func validSourceType(t string) bool {
	return isNavidrome(t) || t == store.SourceABSItem
}

// loadSource reads a card source as owner (the logged-in username).
func (s *Server) loadSource(ctx context.Context, owner, sourceType, sourceID string) (*sourceInfo, error) {
	switch sourceType {
	case store.SourceNavidromePlaylist, store.SourceNavidromeAlbum:
		nav := s.navAs(owner)
		var info sourceInfo
		var songs []navSong
		if sourceType == store.SourceNavidromePlaylist {
			p, err := nav.Playlist(ctx, sourceID)
			if err != nil {
				return nil, err
			}
			info = sourceInfo{Title: p.Name, CoverID: p.CoverArt}
			songs = p.Entry
		} else {
			a, err := nav.Album(ctx, sourceID)
			if err != nil {
				return nil, err
			}
			info = sourceInfo{Title: a.Name, CoverID: a.CoverArt}
			songs = a.Song
		}
		info.Description = "Synced from Navidrome by yoto-proxy"
		info.CoverRev = info.CoverID
		for i, song := range songs {
			info.Tracks = append(info.Tracks, store.Track{
				Position:      i + 1,
				SourceTrackID: song.ID,
				Title:         song.Title,
				Artist:        song.Artist,
				Suffix:        song.Suffix,
				Duration:      song.Duration,
				Mode:          store.ModeStream,
			})
		}
		return &info, nil

	case store.SourceABSItem:
		if s.abs == nil {
			return nil, errABSDisabled
		}
		token, err := s.absToken(ctx, owner)
		if err != nil {
			return nil, err
		}
		item, err := s.abs.Item(ctx, token, sourceID)
		if err != nil {
			return nil, err
		}
		segs, err := abs.Plan(item)
		if err != nil {
			return nil, err
		}
		md := item.Media.Metadata
		info := &sourceInfo{
			Title:       md.Title,
			Description: strings.TrimSpace(md.AuthorName + " · Synced from Audiobookshelf by yoto-proxy"),
			CoverID:     item.ID,
			CoverRev:    item.ID + "@" + strconv.FormatInt(item.UpdatedAt, 10),
		}
		for i, seg := range segs {
			mode := store.ModeCut
			if seg.Whole && seg.Ext == "mp3" {
				mode = store.ModeRaw
			}
			info.Tracks = append(info.Tracks, store.Track{
				Position:      i + 1,
				SourceTrackID: seg.Ino,
				Title:         seg.Title,
				Artist:        md.AuthorName,
				Suffix:        seg.Ext,
				Duration:      int(math.Round(seg.Duration())),
				Start:         seg.Start,
				End:           seg.End,
				Mode:          mode,
			})
		}
		return info, nil
	}
	return nil, fmt.Errorf("unknown source type %q", sourceType)
}

// openCover opens a source's cover image.
func (s *Server) openCover(ctx context.Context, owner, sourceType, coverID string, size int) (io.ReadCloser, error) {
	if sourceType == store.SourceABSItem {
		if s.abs == nil {
			return nil, errABSDisabled
		}
		token, err := s.absToken(ctx, owner)
		if err != nil {
			return nil, err
		}
		resp, err := s.abs.Cover(ctx, token, coverID, size)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	resp, err := s.navAs(owner).CoverArt(ctx, coverID, size)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// coverVersion changes whenever card art rendering changes, forcing re-upload.
const coverVersion = "portrait-v1"

// syncCard refreshes the card's track list from its source and pushes the
// result to Yoto, creating the Yoto playlist on first sync. actor is the
// user doing the sync; it becomes the owner of cards that have none.
func (s *Server) syncCard(ctx context.Context, cardID int64, actor string) (*store.Card, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	card, err := s.store.GetCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*store.Card, error) {
		_ = s.store.MarkSyncError(ctx, cardID, err.Error())
		return nil, err
	}

	if card.Owner == "" && actor != "" {
		if err := s.store.SetCardOwner(ctx, cardID, actor); err != nil {
			return nil, err
		}
		card.Owner = actor
	}

	src, err := s.loadSource(ctx, card.Owner, card.SourceType, card.SourceID)
	if err != nil {
		return fail(err)
	}
	if len(src.Tracks) == 0 {
		return fail(fmt.Errorf("source has no tracks"))
	}

	width := 2
	if len(src.Tracks) > 99 {
		width = 3
	}
	for i := range src.Tracks {
		src.Tracks[i].Key = fmt.Sprintf("%0*d", width, i+1)
	}
	if err := s.store.ReplaceTracks(ctx, cardID, src.Tracks); err != nil {
		return fail(err)
	}

	coverURL := card.CoverURL
	coverKey := coverVersion + ":" + src.CoverRev
	if src.CoverID != "" && (coverURL == "" || card.CoverKey != coverKey) {
		if u, err := s.uploadCover(ctx, s.yotoAcct(card.YotoAccountID), card.Owner, card.SourceType, src.CoverID); err != nil {
			slog.Warn("cover upload failed; syncing without it", "card", cardID, "err", err)
			_ = s.store.SetCardCoverArt(ctx, cardID, src.CoverID)
		} else {
			coverURL = u
			_ = s.store.SetCardCover(ctx, cardID, src.CoverID, coverKey, u)
		}
	}

	content := &yoto.Content{
		CardID:   card.YotoCardID,
		Title:    card.Title,
		Metadata: yoto.Metadata{Description: src.Description},
	}
	if coverURL != "" {
		content.Metadata.Cover = &yoto.Cover{ImageL: coverURL}
	}
	needsCache := false
	for _, t := range src.Tracks {
		if t.Mode == store.ModeCut {
			needsCache = true
		}
		label := strconv.Itoa(t.Position)
		content.Content.Chapters = append(content.Content.Chapters, yoto.Chapter{
			Key:          t.Key,
			Title:        t.Title,
			OverlayLabel: label,
			Duration:     t.Duration,
			Display:      &yoto.Display{Icon16x16: yoto.DefaultIcon},
			Tracks: []yoto.Track{{
				Key:          t.Key,
				Title:        t.Title,
				TrackURL:     fmt.Sprintf("%s/s/%s/%s.mp3", s.cfg.PublicURL, card.StreamToken, t.Key),
				Type:         "stream",
				Format:       "mp3",
				Duration:     t.Duration,
				OverlayLabel: label,
				Display:      &yoto.Display{Icon16x16: yoto.DefaultIcon},
			}},
		})
	}

	yotoID, err := s.yotoAcct(card.YotoAccountID).SaveContent(ctx, content)
	if err != nil {
		return fail(err)
	}
	if err := s.store.MarkSynced(ctx, cardID, yotoID); err != nil {
		return nil, err
	}
	slog.Info("synced card", "card", cardID, "yotoCardId", yotoID, "tracks", len(src.Tracks))
	if needsCache {
		s.cache.Enqueue(cardID)
	}
	return s.store.GetCard(ctx, cardID)
}

// uploadCover renders the source art as portrait card art and uploads it.
func (s *Server) uploadCover(ctx context.Context, acct *yoto.Account, owner, sourceType, coverID string) (string, error) {
	body, err := s.openCover(ctx, owner, sourceType, coverID, 640)
	if err != nil {
		return "", err
	}
	defer body.Close()
	img, err := cover.Portrait(body)
	if err != nil {
		return "", err
	}
	return acct.UploadCover(ctx, bytes.NewReader(img), "image/jpeg")
}
