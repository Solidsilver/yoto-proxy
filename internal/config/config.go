// Package config loads runtime settings from the environment. Secrets can be
// given directly (FOO) or as a path to a file (FOO_FILE), which is how
// agenix/systemd LoadCredential hand them over.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr string // e.g. 127.0.0.1:3614
	PublicURL  string // externally reachable base URL, no trailing slash
	DataDir    string // holds the SQLite database

	NavidromeURL      string
	NavidromeAuth     string // "password" (one fixed user) or "header" (per-user ExtAuth)
	NavidromeUser     string // password mode
	NavidromePassword string // password mode
	NavidromeHeader   string // header mode: ExtAuth user header
	NavidromeSource   string // header mode: local IP to connect from (in ExtAuth.TrustedSources)
	NavidromeUserMap  map[string]string // header mode: login username -> Navidrome username
	TranscodeBitrate  int // kbps used when the source file is not already mp3

	ABSURL       string // Audiobookshelf API base (internal)
	ABSPublicURL string // Audiobookshelf browser URL; empty disables Audiobookshelf
	ABSBitrate   int    // kbps (mono) for chapters that must be re-encoded
	FFmpegPath   string

	YotoClientID     string
	YotoClientSecret string // optional; empty for public (PKCE-only) clients

	AuthDisabled     bool // local development only
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:   env("LISTEN_ADDR", "127.0.0.1:3614"),
		PublicURL:    strings.TrimRight(env("PUBLIC_URL", "http://localhost:3614"), "/"),
		DataDir:      env("DATA_DIR", "./data"),
		NavidromeURL: strings.TrimRight(env("NAVIDROME_URL", "http://127.0.0.1:4533"), "/"),
		NavidromeAuth:   env("NAVIDROME_AUTH", "password"),
		NavidromeUser:   env("NAVIDROME_USER", ""),
		NavidromeHeader: env("NAVIDROME_USER_HEADER", "Remote-User"),
		NavidromeSource: env("NAVIDROME_SOURCE_ADDR", ""),
		YotoClientID:  env("YOTO_CLIENT_ID", ""),
		ABSURL:        strings.TrimRight(env("ABS_URL", "http://127.0.0.1:13378"), "/"),
		ABSPublicURL:  strings.TrimRight(env("ABS_PUBLIC_URL", ""), "/"),
		FFmpegPath:    env("FFMPEG_PATH", "ffmpeg"),
		OIDCIssuer:    env("OIDC_ISSUER", ""),
		OIDCClientID:  env("OIDC_CLIENT_ID", ""),
	}
	var err error
	if c.NavidromePassword, err = secret("NAVIDROME_PASSWORD"); err != nil {
		return nil, err
	}
	if c.YotoClientSecret, err = secret("YOTO_CLIENT_SECRET"); err != nil {
		return nil, err
	}
	if c.OIDCClientSecret, err = secret("OIDC_CLIENT_SECRET"); err != nil {
		return nil, err
	}
	c.TranscodeBitrate, err = strconv.Atoi(env("TRANSCODE_BITRATE", "192"))
	if err != nil {
		return nil, fmt.Errorf("TRANSCODE_BITRATE: %w", err)
	}
	if c.ABSBitrate, err = strconv.Atoi(env("ABS_BITRATE", "64")); err != nil {
		return nil, fmt.Errorf("ABS_BITRATE: %w", err)
	}
	c.AuthDisabled, _ = strconv.ParseBool(env("AUTH_DISABLED", "false"))
	if c.NavidromeUserMap, err = parseMap(env("NAVIDROME_USER_MAP", "")); err != nil {
		return nil, fmt.Errorf("NAVIDROME_USER_MAP: %w", err)
	}

	var missing []string
	required := map[string]string{"YOTO_CLIENT_ID": c.YotoClientID}
	switch c.NavidromeAuth {
	case "password":
		required["NAVIDROME_USER"] = c.NavidromeUser
		required["NAVIDROME_PASSWORD"] = c.NavidromePassword
	case "header":
	default:
		return nil, fmt.Errorf("NAVIDROME_AUTH must be \"password\" or \"header\", got %q", c.NavidromeAuth)
	}
	for name, v := range required {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if !c.AuthDisabled {
		for name, v := range map[string]string{
			"OIDC_ISSUER":        c.OIDCIssuer,
			"OIDC_CLIENT_ID":     c.OIDCClientID,
			"OIDC_CLIENT_SECRET": c.OIDCClientSecret,
		} {
			if v == "" {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

// parseMap parses "a=b,c=d".
func parseMap(s string) (map[string]string, error) {
	m := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("bad entry %q (want login=navidrome)", pair)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func secret(key string) (string, error) {
	if path := os.Getenv(key + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(key), nil
}
