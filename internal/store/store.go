// Package store persists cards, their track lists, and app settings
// (Yoto tokens, session key) in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cards (
	id             INTEGER PRIMARY KEY,
	title          TEXT NOT NULL,
	source_type    TEXT NOT NULL,
	source_id      TEXT NOT NULL,
	stream_token   TEXT NOT NULL UNIQUE,
	yoto_card_id   TEXT NOT NULL DEFAULT '',
	cover_art_id   TEXT NOT NULL DEFAULT '',
	cover_url      TEXT NOT NULL DEFAULT '',
	last_synced_at TEXT NOT NULL DEFAULT '',
	last_error     TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS card_tracks (
	card_id         INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	key             TEXT NOT NULL,
	position        INTEGER NOT NULL,
	source_track_id TEXT NOT NULL,
	title           TEXT NOT NULL,
	artist          TEXT NOT NULL DEFAULT '',
	suffix          TEXT NOT NULL DEFAULT '',
	duration        INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (card_id, key)
);
`

// Source types a card can be built from.
const (
	SourceNavidromePlaylist = "navidrome_playlist"
	SourceNavidromeAlbum    = "navidrome_album"
	SourceABSItem           = "abs_item"
)

// Track modes.
const (
	ModeStream = ""    // stream the source track (Navidrome: raw mp3 or transcode)
	ModeRaw    = "raw" // pass a whole mp3 file through unchanged
	ModeCut    = "cut" // a time range of a file, rendered to mp3 (cached)
)

type Card struct {
	ID            int64  `json:"id"`
	YotoAccountID int64  `json:"-"`
	Title         string `json:"title"`
	SourceType   string `json:"sourceType"`
	SourceID     string `json:"sourceId"`
	Owner        string `json:"owner"`
	StreamToken  string `json:"-"`
	YotoCardID   string `json:"yotoCardId"`
	CoverArtID   string `json:"coverArtId"`
	CoverURL     string `json:"coverUrl"`
	CoverKey     string `json:"-"`
	LastSyncedAt string `json:"lastSyncedAt"`
	LastError    string `json:"lastError"`
	CreatedAt    string `json:"createdAt"`
	TrackCount   int    `json:"trackCount"`
}

type Track struct {
	Key           string `json:"key"`
	Position      int    `json:"position"`
	SourceTrackID string `json:"sourceTrackId"`
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	Suffix        string  `json:"suffix"`
	Duration      int     `json:"duration"`
	Start         float64 `json:"start,omitempty"` // ModeCut: seconds into the file
	End           float64 `json:"end,omitempty"`
	Mode          string  `json:"mode,omitempty"`
}

type Store struct{ db *sql.DB }

// Columns added after the first release; ADD COLUMN fails harmlessly if present.
var migrations = []string{
	`ALTER TABLE cards ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cards ADD COLUMN cover_key TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE card_tracks ADD COLUMN seg_start REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE card_tracks ADD COLUMN seg_end REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE card_tracks ADD COLUMN mode TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE cards ADD COLUMN yoto_account_id INTEGER NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS yoto_accounts (
		id         INTEGER PRIMARY KEY,
		subject    TEXT NOT NULL DEFAULT '',
		label      TEXT NOT NULL,
		tokens     TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS yoto_accounts_subject ON yoto_accounts(subject) WHERE subject != ''`,
	`CREATE TABLE IF NOT EXISTS yoto_members (
		login_user TEXT PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES yoto_accounts(id) ON DELETE CASCADE,
		joined_at  TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS yoto_invites (
		token      TEXT PRIMARY KEY,
		account_id INTEGER NOT NULL REFERENCES yoto_accounts(id) ON DELETE CASCADE,
		created_by TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS abs_accounts (
		login_user    TEXT PRIMARY KEY,
		abs_user_id   TEXT NOT NULL,
		abs_username  TEXT NOT NULL,
		abs_type      TEXT NOT NULL,
		kind          TEXT NOT NULL,
		token         TEXT NOT NULL,
		refresh_token TEXT NOT NULL DEFAULT '',
		updated_at    TEXT NOT NULL
	)`,
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "yoto-proxy.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // keep SQLite writes serialized
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// --- settings ---

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// SecretSetting returns a random secret stored under key, creating it on first use.
func (s *Store) SecretSetting(ctx context.Context, key string) ([]byte, error) {
	v, err := s.GetSetting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		v = RandomToken(32)
		if err := s.SetSetting(ctx, key, v); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return base64.RawURLEncoding.DecodeString(v)
}

// RandomToken returns n random bytes, base64url encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- cards ---

const cardCols = `c.id, c.yoto_account_id, c.title, c.source_type, c.source_id, c.owner, c.stream_token, c.yoto_card_id,
	c.cover_art_id, c.cover_url, c.cover_key, c.last_synced_at, c.last_error, c.created_at,
	(SELECT COUNT(*) FROM card_tracks t WHERE t.card_id = c.id)`

func scanCard(row interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.YotoAccountID, &c.Title, &c.SourceType, &c.SourceID, &c.Owner, &c.StreamToken, &c.YotoCardID,
		&c.CoverArtID, &c.CoverURL, &c.CoverKey, &c.LastSyncedAt, &c.LastError, &c.CreatedAt, &c.TrackCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// ListCards lists the cards belonging to a Yoto account.
func (s *Store) ListCards(ctx context.Context, yotoAccountID int64) ([]Card, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cardCols+` FROM cards c WHERE c.yoto_account_id = ?
		ORDER BY c.title COLLATE NOCASE`, yotoAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := []Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, *c)
	}
	return cards, rows.Err()
}

func (s *Store) GetCard(ctx context.Context, id int64) (*Card, error) {
	return scanCard(s.db.QueryRowContext(ctx, `SELECT `+cardCols+` FROM cards c WHERE c.id = ?`, id))
}

func (s *Store) GetCardByToken(ctx context.Context, token string) (*Card, error) {
	return scanCard(s.db.QueryRowContext(ctx, `SELECT `+cardCols+` FROM cards c WHERE c.stream_token = ?`, token))
}

func (s *Store) CreateCard(ctx context.Context, yotoAccountID int64, title, sourceType, sourceID, owner string) (*Card, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO cards (yoto_account_id, title, source_type, source_id, owner, stream_token, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		yotoAccountID, title, sourceType, sourceID, owner, RandomToken(24), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetCard(ctx, id)
}

func (s *Store) UpdateCardTitle(ctx context.Context, id int64, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET title = ? WHERE id = ?`, title, id)
	return err
}

func (s *Store) RotateStreamToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET stream_token = ? WHERE id = ?`, RandomToken(24), id)
	return err
}

func (s *Store) DeleteCard(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cards WHERE id = ?`, id)
	return err
}

func (s *Store) SetCardCover(ctx context.Context, id int64, coverArtID, coverKey, coverURL string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET cover_art_id = ?, cover_key = ?, cover_url = ? WHERE id = ?`,
		coverArtID, coverKey, coverURL, id)
	return err
}

// SetCardCoverArt records the source cover id without uploading anything.
func (s *Store) SetCardCoverArt(ctx context.Context, id int64, coverArtID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET cover_art_id = ? WHERE id = ?`, coverArtID, id)
	return err
}

func (s *Store) SetCardOwner(ctx context.Context, id int64, owner string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET owner = ? WHERE id = ?`, owner, id)
	return err
}

func (s *Store) MarkSynced(ctx context.Context, id int64, yotoCardID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cards SET yoto_card_id = ?, last_synced_at = ?, last_error = '' WHERE id = ?`,
		yotoCardID, now(), id)
	return err
}

func (s *Store) MarkSyncError(ctx context.Context, id int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET last_error = ? WHERE id = ?`, msg, id)
	return err
}

// --- tracks ---

func (s *Store) ReplaceTracks(ctx context.Context, cardID int64, tracks []Track) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM card_tracks WHERE card_id = ?`, cardID); err != nil {
		return err
	}
	for _, t := range tracks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO card_tracks (card_id, key, position, source_track_id, title, artist, suffix, duration, seg_start, seg_end, mode)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cardID, t.Key, t.Position, t.SourceTrackID, t.Title, t.Artist, t.Suffix, t.Duration, t.Start, t.End, t.Mode); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListTracks(ctx context.Context, cardID int64) ([]Track, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, position, source_track_id, title, artist, suffix, duration, seg_start, seg_end, mode
		 FROM card_tracks WHERE card_id = ? ORDER BY position`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tracks := []Track{}
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.Key, &t.Position, &t.SourceTrackID, &t.Title, &t.Artist, &t.Suffix, &t.Duration, &t.Start, &t.End, &t.Mode); err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (s *Store) GetTrack(ctx context.Context, cardID int64, key string) (*Track, error) {
	var t Track
	err := s.db.QueryRowContext(ctx,
		`SELECT key, position, source_track_id, title, artist, suffix, duration, seg_start, seg_end, mode
		 FROM card_tracks WHERE card_id = ? AND key = ?`, cardID, key).
		Scan(&t.Key, &t.Position, &t.SourceTrackID, &t.Title, &t.Artist, &t.Suffix, &t.Duration, &t.Start, &t.End, &t.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

// --- Yoto accounts, members and invites ---
//
// Each Yoto account can be shared by several logged-in users (members);
// cards belong to a Yoto account. A user is in at most one Yoto account.

type YotoAccount struct {
	ID        int64
	Subject   string // Yoto user id; joins people who connect the same account
	Label     string
	Tokens    string // JSON; empty when disconnected
	CreatedBy string
}

func (s *Store) CreateYotoAccount(ctx context.Context, subject, label, tokens, createdBy string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO yoto_accounts (subject, label, tokens, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		subject, label, tokens, createdBy, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) scanYotoAccount(row *sql.Row) (*YotoAccount, error) {
	var a YotoAccount
	err := row.Scan(&a.ID, &a.Subject, &a.Label, &a.Tokens, &a.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

const yotoAccountCols = `id, subject, label, tokens, created_by`

func (s *Store) GetYotoAccount(ctx context.Context, id int64) (*YotoAccount, error) {
	return s.scanYotoAccount(s.db.QueryRowContext(ctx,
		`SELECT `+yotoAccountCols+` FROM yoto_accounts WHERE id = ?`, id))
}

func (s *Store) GetYotoAccountBySubject(ctx context.Context, subject string) (*YotoAccount, error) {
	if subject == "" {
		return nil, ErrNotFound
	}
	return s.scanYotoAccount(s.db.QueryRowContext(ctx,
		`SELECT `+yotoAccountCols+` FROM yoto_accounts WHERE subject = ?`, subject))
}

func (s *Store) CountYotoAccounts(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM yoto_accounts`).Scan(&n)
	return n, err
}

func (s *Store) SetYotoTokens(ctx context.Context, id int64, tokens string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE yoto_accounts SET tokens = ? WHERE id = ?`, tokens, id)
	return err
}

func (s *Store) SetYotoLabel(ctx context.Context, id int64, label string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE yoto_accounts SET label = ? WHERE id = ?`, label, id)
	return err
}

// MemberAccount returns the Yoto account id a user belongs to.
func (s *Store) MemberAccount(ctx context.Context, loginUser string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT account_id FROM yoto_members WHERE login_user = ?`, loginUser).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) SetMember(ctx context.Context, loginUser string, accountID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO yoto_members (login_user, account_id, joined_at) VALUES (?, ?, ?)
		 ON CONFLICT(login_user) DO UPDATE SET account_id = excluded.account_id, joined_at = excluded.joined_at`,
		loginUser, accountID, now())
	return err
}

func (s *Store) RemoveMember(ctx context.Context, loginUser string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM yoto_members WHERE login_user = ?`, loginUser)
	return err
}

func (s *Store) ListMembers(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT login_user FROM yoto_members WHERE account_id = ? ORDER BY joined_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AssignUnownedCards moves cards from before Yoto accounts existed (account 0).
func (s *Store) AssignUnownedCards(ctx context.Context, accountID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cards SET yoto_account_id = ? WHERE yoto_account_id = 0`, accountID)
	return err
}

// CardOwners lists the distinct owners of a Yoto account's cards.
func (s *Store) CardOwners(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT owner FROM cards WHERE yoto_account_id = ? AND owner != ''`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

type Invite struct {
	Token     string
	AccountID int64
	CreatedBy string
	ExpiresAt time.Time
}

func (s *Store) CreateInvite(ctx context.Context, accountID int64, createdBy string, ttl time.Duration) (*Invite, error) {
	inv := &Invite{Token: RandomToken(18), AccountID: accountID, CreatedBy: createdBy,
		ExpiresAt: time.Now().Add(ttl).UTC().Truncate(time.Second)}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO yoto_invites (token, account_id, created_by, expires_at) VALUES (?, ?, ?, ?)`,
		inv.Token, inv.AccountID, inv.CreatedBy, inv.ExpiresAt.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// GetInvite returns an unexpired invite.
func (s *Store) GetInvite(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	var exp string
	err := s.db.QueryRowContext(ctx,
		`SELECT token, account_id, created_by, expires_at FROM yoto_invites WHERE token = ?`, token).
		Scan(&inv.Token, &inv.AccountID, &inv.CreatedBy, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.ExpiresAt, _ = time.Parse(time.RFC3339, exp)
	if time.Now().After(inv.ExpiresAt) {
		_ = s.DeleteInvite(ctx, token)
		return nil, ErrNotFound
	}
	return &inv, nil
}

func (s *Store) DeleteInvite(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM yoto_invites WHERE token = ?`, token)
	return err
}

// --- Audiobookshelf accounts ---

// Account kinds.
const (
	ABSKindAPIKey  = "api_key" // non-expiring key created for the user
	ABSKindSession = "session" // access + refresh tokens, refreshed in the background
)

type ABSAccount struct {
	LoginUser    string
	ABSUserID    string
	ABSUsername  string
	ABSType      string
	Kind         string
	Token        string // API key or access token
	RefreshToken string
	UpdatedAt    string
}

func (s *Store) GetABSAccount(ctx context.Context, loginUser string) (*ABSAccount, error) {
	var a ABSAccount
	err := s.db.QueryRowContext(ctx,
		`SELECT login_user, abs_user_id, abs_username, abs_type, kind, token, refresh_token, updated_at
		 FROM abs_accounts WHERE login_user = ?`, loginUser).
		Scan(&a.LoginUser, &a.ABSUserID, &a.ABSUsername, &a.ABSType, &a.Kind, &a.Token, &a.RefreshToken, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (s *Store) ListABSAccounts(ctx context.Context) ([]ABSAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT login_user, abs_user_id, abs_username, abs_type, kind, token, refresh_token, updated_at FROM abs_accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ABSAccount
	for rows.Next() {
		var a ABSAccount
		if err := rows.Scan(&a.LoginUser, &a.ABSUserID, &a.ABSUsername, &a.ABSType, &a.Kind, &a.Token, &a.RefreshToken, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SaveABSAccount(ctx context.Context, a *ABSAccount) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO abs_accounts (login_user, abs_user_id, abs_username, abs_type, kind, token, refresh_token, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(login_user) DO UPDATE SET abs_user_id = excluded.abs_user_id,
		   abs_username = excluded.abs_username, abs_type = excluded.abs_type, kind = excluded.kind,
		   token = excluded.token, refresh_token = excluded.refresh_token, updated_at = excluded.updated_at`,
		a.LoginUser, a.ABSUserID, a.ABSUsername, a.ABSType, a.Kind, a.Token, a.RefreshToken, now())
	return err
}

func (s *Store) DeleteABSAccount(ctx context.Context, loginUser string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM abs_accounts WHERE login_user = ?`, loginUser)
	return err
}

// CountCardsForSource counts cards built from the same source (used to
// decide whether shared cache files can be removed).
func (s *Store) CountCardsForSource(ctx context.Context, sourceType, sourceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cards WHERE source_type = ? AND source_id = ?`,
		sourceType, sourceID).Scan(&n)
	return n, err
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
