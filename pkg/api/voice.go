package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRefreshWait = 20
	maxRefreshWait     = 25
	defaultNewDays     = 3
	maxNewDays         = 30
	defaultNewLimit    = 5
	maxNewLimit        = 20
)

type newVideoResponse struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Channel   string `json:"channel"`
	Duration  int    `json:"duration"`
	URL       string `json:"url"`
	Published string `json:"published,omitempty"`
}

// intParam reads a query value, falling back to def when it is absent or not
// a number, and clamps the result into [min, max].
func intParam(r *http.Request, name string, def, min, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		n = def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) refreshAllSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	wait := time.Duration(intParam(r, "wait", defaultRefreshWait, 0, maxRefreshWait)) * time.Second
	s.respond(w, Response{Success: true, Data: s.agent.RefreshAll(r.Context(), wait)})
}

func (s *Server) newVideosHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	days := intParam(r, "days", defaultNewDays, 1, maxNewDays)
	limit := intParam(r, "limit", defaultNewLimit, 1, maxNewLimit)

	videos, total, err := s.db.ListRecentNewVideos(time.Now().Add(-time.Duration(days)*24*time.Hour), limit)
	if err != nil {
		s.respondError(w, "Failed to list new videos", http.StatusInternalServerError)
		return
	}

	items := make([]newVideoResponse, 0, len(videos))
	for _, v := range videos {
		item := newVideoResponse{ID: v.ID, Title: v.Title, Channel: v.ChannelName, Duration: v.Duration, URL: v.URL}
		if !v.Published.IsZero() {
			item.Published = v.Published.Format(time.RFC3339)
		}
		items = append(items, item)
	}

	s.respond(w, Response{Success: true, Data: map[string]interface{}{"videos": items, "total": total}})
}

func (s *Server) videoHandler(w http.ResponseWriter, r *http.Request) {
	rest := extractID(r.URL.Path, "/api/videos/")
	id, ok := strings.CutSuffix(rest, "/download")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	videoID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	s.downloadVideo(w, videoID)
}

// downloadVideo starts a download of a stored video. It is idempotent: a video
// that is downloaded or already queued is reported, not downloaded again.
func (s *Server) downloadVideo(w http.ResponseWriter, videoID int64) {
	video, err := s.db.GetVideo(videoID)
	if errors.Is(err, sql.ErrNoRows) {
		s.respondError(w, "Video not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.respondError(w, "Failed to load video", http.StatusInternalServerError)
		return
	}

	s.startMu.Lock()
	defer s.startMu.Unlock()

	if downloaded, _ := s.db.GetDownloadedVideoURLs(); downloaded[video.URL] {
		s.respond(w, Response{Success: true, Data: map[string]interface{}{"status": "downloaded"}})
		return
	}

	active, err := s.db.ListDownloadsByStatus("pending", "downloading")
	if err != nil {
		s.respondError(w, "Failed to check downloads", http.StatusInternalServerError)
		return
	}
	for _, d := range active {
		if d.URL == video.URL {
			s.respond(w, Response{Success: true, Data: map[string]interface{}{"status": "queued", "download_id": d.ID}})
			return
		}
	}

	channelName := ""
	if ch, err := s.db.GetChannel(video.ChannelID); err == nil {
		channelName = ch.Name
	}
	d, err := s.startDownload(video.URL, video.Title, channelName)
	if err != nil {
		s.respondError(w, "Failed to create download", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]interface{}{"status": "started", "download_id": d.ID, "title": d.Title}})
}
