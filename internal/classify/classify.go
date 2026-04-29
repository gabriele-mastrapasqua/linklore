// Package classify assigns a type label (article | video | image |
// audio | document | book) to a link from the cheapest signal
// available. URL-only is enough for most well-known hosts and file
// extensions; the worker can later refine the value from og:type or
// the response Content-Type if needed.
package classify

import (
	"net/url"
	"path"
	"strings"
)

const (
	KindArticle  = "article"
	KindVideo    = "video"
	KindImage    = "image"
	KindAudio    = "audio"
	KindDocument = "document"
	KindBook     = "book"
)

// FromURL is the cheapest classifier: host + path-extension only.
// Returns KindArticle when nothing matches — refine from headers or
// og:type later.
func FromURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return KindArticle
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")

	switch {
	case host == "youtube.com" || host == "youtu.be" ||
		strings.HasSuffix(host, ".youtube.com") ||
		host == "vimeo.com" || strings.HasSuffix(host, ".vimeo.com") ||
		host == "twitch.tv" || strings.HasSuffix(host, ".twitch.tv") ||
		host == "ted.com" || host == "tiktok.com":
		return KindVideo
	case host == "soundcloud.com" || strings.HasSuffix(host, ".soundcloud.com") ||
		host == "spotify.com" || strings.HasSuffix(host, ".spotify.com") ||
		host == "open.spotify.com" || host == "anchor.fm" ||
		host == "podcasts.apple.com":
		return KindAudio
	case host == "imgur.com" || strings.HasSuffix(host, ".imgur.com") ||
		host == "flickr.com" || strings.HasSuffix(host, ".flickr.com"):
		return KindImage
	}

	switch strings.ToLower(path.Ext(u.Path)) {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return KindVideo
	case ".mp3", ".wav", ".aac", ".flac", ".ogg", ".m4a":
		return KindAudio
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".svg":
		return KindImage
	case ".pdf":
		return KindDocument
	case ".epub", ".mobi", ".azw3":
		return KindBook
	case ".doc", ".docx", ".odt", ".rtf", ".txt", ".md":
		return KindDocument
	}

	return KindArticle
}

// FromOGType refines a kind from an `og:type` meta tag value when one
// is present. Falls back to the cheap URL-based default when nothing
// useful is encoded.
func FromOGType(ogType, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(ogType)) {
	case "video", "video.movie", "video.episode", "video.tv_show", "video.other":
		return KindVideo
	case "music", "music.song", "music.album", "music.playlist", "music.radio_station":
		return KindAudio
	case "image":
		return KindImage
	case "book":
		return KindBook
	case "article", "blog", "website":
		return KindArticle
	}
	return fallback
}
