package api

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// validVideoURL keeps the subtitle endpoints to plain web links.
func validVideoURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) subtitleLangsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	videoURL := r.URL.Query().Get("url")
	if !validVideoURL(videoURL) {
		s.respondError(w, "Valid url is required", http.StatusBadRequest)
		return
	}
	langs, err := s.downloader.SubtitleLangs(r.Context(), videoURL)
	if err != nil {
		log.Printf("subtitle langs %s: %v", videoURL, err)
		s.respondError(w, "Failed to get subtitle languages", http.StatusBadGateway)
		return
	}
	s.respond(w, Response{Success: true, Data: langs})
}

// subtitlesHandler streams one SRT file back as a download; nothing is stored.
func (s *Server) subtitlesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	videoURL, lang := q.Get("url"), q.Get("lang")
	if !validVideoURL(videoURL) {
		s.respondError(w, "Valid url is required", http.StatusBadRequest)
		return
	}
	if !validLangParam(lang) {
		s.respondError(w, "Invalid lang", http.StatusBadRequest)
		return
	}
	name, data, err := s.downloader.Subtitles(r.Context(), videoURL, lang, q.Get("auto") == "1")
	if err != nil {
		log.Printf("subtitles %s [%s]: %v", videoURL, lang, err)
		s.respondError(w, "Failed to download subtitles", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.NewReplacer(`"`, "", "\\", "", "\n", "", "\r", "").Replace(name)))
	_, _ = w.Write(data)
}

func validLangParam(lang string) bool {
	if lang == "" || lang[0] == '-' {
		return false
	}
	for _, c := range lang {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
