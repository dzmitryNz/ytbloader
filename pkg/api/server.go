package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitry/ytbloader/pkg/agent"
	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
)

type Server struct {
	server     *http.Server
	db         *db.DB
	downloader *downloader.Downloader
	agent      *agent.Agent
}

type DownloadRequest struct {
	URL      string `json:"url"`
	OutputDir string `json:"output_dir,omitempty"`
}

type TaskRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type Response struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func NewServer(addr string, database *db.DB, dl *downloader.Downloader, agt *agent.Agent, webDir string) *Server {
	s := &Server{
		db:         database,
		downloader: dl,
		agent:      agt,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.healthHandler)
	mux.HandleFunc("/api/downloads", s.downloadsHandler)
	mux.HandleFunc("/api/downloads/", s.downloadHandler)
	mux.HandleFunc("/api/tasks", s.tasksHandler)
	mux.HandleFunc("/api/tasks/", s.taskHandler)
	mux.HandleFunc("/api/subscriptions", s.subscriptionsHandler)
	mux.HandleFunc("/api/subscriptions/", s.subscriptionHandler)
	mux.HandleFunc("/api/channels/", s.channelVideosHandler)
	mux.HandleFunc("/api/video-downloads", s.videoDownloadsHandler)

	if webDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(webDir)))
	}

	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s
}

func (s *Server) Start() error {
	log.Printf("API server starting on %s", s.server.Addr)
	return s.server.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	s.respond(w, Response{Success: true})
}

func (s *Server) downloadsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listDownloads(w, r)
	case http.MethodPost:
		s.createDownload(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) downloadHandler(w http.ResponseWriter, r *http.Request) {
	id := extractID(r.URL.Path, "/api/downloads/")
	if id == "" {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if strings.HasSuffix(id, "/file") {
		s.serveFile(w, r, id[:len(id)-5])
		return
	}
	if strings.HasSuffix(id, "/cancel") {
		s.cancelDownload(w, r, id[:len(id)-len("/cancel")])
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getDownload(w, r, id)
	case http.MethodPut:
		s.retryDownload(w, r, id)
	case http.MethodDelete:
		s.deleteDownload(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) tasksHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listTasks(w, r)
	case http.MethodPost:
		s.createTask(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) taskHandler(w http.ResponseWriter, r *http.Request) {
	id := extractID(r.URL.Path, "/api/tasks/")
	if id == "" {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getTask(w, r, id)
	case http.MethodPut:
		s.updateTask(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	downloads, err := s.db.ListDownloads()
	if err != nil {
		s.respondError(w, "Failed to list downloads", http.StatusInternalServerError)
		return
	}
	s.respond(w, Response{Success: true, Data: downloads})
}

func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	var req DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	title, channelName, _ := s.downloader.GetInfo(r.Context(), req.URL)

	d := &db.Download{
		URL:         req.URL,
		Title:       title,
		ChannelName: channelName,
		Status:      "pending",
	}

	if err := s.db.CreateDownload(d); err != nil {
		s.respondError(w, "Failed to create download", http.StatusInternalServerError)
		return
	}

	s.agent.ProcessDownload(context.Background(), d)

	s.respond(w, Response{Success: true, Data: d})
}

func (s *Server) getDownload(w http.ResponseWriter, r *http.Request, id string) {
	var downloadID int64
	if _, err := fmt.Sscanf(id, "%d", &downloadID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	d, err := s.db.GetDownload(downloadID)
	if err != nil {
		s.respondError(w, "Download not found", http.StatusNotFound)
		return
	}

	s.respond(w, Response{Success: true, Data: d})
}

func (s *Server) retryDownload(w http.ResponseWriter, r *http.Request, id string) {
	var downloadID int64
	if _, err := fmt.Sscanf(id, "%d", &downloadID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	d, err := s.db.GetDownload(downloadID)
	if err != nil {
		s.respondError(w, "Download not found", http.StatusNotFound)
		return
	}

	if d.Status != "pending" && d.Status != "failed" && d.Status != "cancelled" {
		s.respondError(w, fmt.Sprintf("Cannot retry download in '%s' status", d.Status), http.StatusBadRequest)
		return
	}

	_ = s.db.UpdateDownloadStatus(d.ID, "pending")
	go s.agent.ProcessDownload(context.Background(), d)

	s.respond(w, Response{Success: true, Data: d})
}

func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request, id string) {
	var downloadID int64
	if _, err := fmt.Sscanf(id, "%d", &downloadID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteDownload(downloadID); err != nil {
		s.respondError(w, "Failed to delete download", http.StatusInternalServerError)
		return
	}

	s.respond(w, Response{Success: true})
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, id string) {
	var downloadID int64
	if _, err := fmt.Sscanf(id, "%d", &downloadID); err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	d, err := s.db.GetDownload(downloadID)
	if err != nil {
		http.Error(w, "Download not found", http.StatusNotFound)
		return
	}

	if d.OutputPath == "" {
		http.Error(w, "File not available", http.StatusNotFound)
		return
	}

	if _, err := os.Stat(d.OutputPath); os.IsNotExist(err) {
		s.respondError(w, "File not found on disk", http.StatusNotFound)
		return
	}

	name := filepath.Base(d.OutputPath)
	if len(name) > 200 {
		name = name[:200] + filepath.Ext(name)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	http.ServeFile(w, r, d.OutputPath)
}

func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request, id string) {
	var downloadID int64
	if _, err := fmt.Sscanf(id, "%d", &downloadID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	cancelled := s.agent.CancelDownload(downloadID)
	_ = s.db.UpdateDownloadError(downloadID, "cancelled", "")

	msg := "Download cancelled"
	if !cancelled {
		msg = "Download was not active"
	}
	s.respond(w, Response{Success: true, Data: msg})
}

func (s *Server) subscriptionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSubscriptions(w, r)
	case http.MethodPost:
		s.createSubscription(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) subscriptionHandler(w http.ResponseWriter, r *http.Request) {
	id := extractID(r.URL.Path, "/api/subscriptions/")
	if id == "" {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if strings.HasSuffix(id, "/refresh") {
		s.refreshSubscription(w, r, id[:len(id)-len("/refresh")])
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.deleteSubscription(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) channelVideosHandler(w http.ResponseWriter, r *http.Request) {
	id := extractID(r.URL.Path, "/api/channels/")
	if id == "" {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var channelID int64
	if _, err := fmt.Sscanf(id, "%d", &channelID); err != nil {
		s.respondError(w, "Invalid channel ID", http.StatusBadRequest)
		return
	}

	_ = s.db.ResetChannelNewVideosCount(channelID)

	limit := 50
	videos, err := s.db.GetVideosByChannel(channelID, limit)
	if err != nil {
		s.respondError(w, "Failed to list videos", http.StatusInternalServerError)
		return
	}

	downloadedURLs, _ := s.db.GetDownloadedVideoURLs()

	type VideoResponse struct {
		db.Video
		IsDownloaded bool `json:"IsDownloaded"`
	}

	var response []VideoResponse
	for _, v := range videos {
		response = append(response, VideoResponse{
			Video:        v,
			IsDownloaded: downloadedURLs[v.URL],
		})
	}

	s.respond(w, Response{Success: true, Data: response})
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	channels, err := s.db.ListAllChannels()
	if err != nil {
		s.respondError(w, "Failed to list subscriptions", http.StatusInternalServerError)
		return
	}
	s.respond(w, Response{Success: true, Data: channels})
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		s.respondError(w, "URL is required", http.StatusBadRequest)
		return
	}

	existing, _ := s.db.GetChannelByURL(req.URL, "", 0)
	if existing != nil {
		s.respondError(w, "Already subscribed", http.StatusConflict)
		return
	}

	info, err := s.downloader.GetChannelInfo(r.Context(), req.URL)
	if err != nil {
		s.respondError(w, fmt.Sprintf("Failed to get channel info: %v", err), http.StatusBadRequest)
		return
	}

	channel := &db.Channel{
		URL:  req.URL,
		Name: info.Name,
	}

	if err := s.db.CreateChannel(channel); err != nil {
		s.respondError(w, "Failed to create subscription", http.StatusInternalServerError)
		return
	}

	s.respond(w, Response{Success: true, Data: channel})
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request, id string) {
	var channelID int64
	if _, err := fmt.Sscanf(id, "%d", &channelID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteChannel(channelID); err != nil {
		s.respondError(w, "Failed to delete subscription", http.StatusInternalServerError)
		return
	}

	s.respond(w, Response{Success: true})
}

func (s *Server) refreshSubscription(w http.ResponseWriter, r *http.Request, id string) {
	var channelID int64
	if _, err := fmt.Sscanf(id, "%d", &channelID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	channel, err := s.db.GetChannel(channelID)
	if err != nil {
		s.respondError(w, "Channel not found", http.StatusNotFound)
		return
	}

	oldLastCheck := channel.LastCheck

	videos, err := s.downloader.GetChannelVideos(r.Context(), channel.URL, 30)
	if err != nil {
		s.respondError(w, fmt.Sprintf("Failed to fetch videos: %v", err), http.StatusInternalServerError)
		return
	}

	for i, v := range videos {
		video := &db.Video{
			ChannelID: channelID,
			URL:       v.URL,
			Title:     v.Title,
			Duration:  v.Duration,
			Position:  i + 1,
		}
		s.db.UpsertVideo(video)
	}

	dbVideos, _ := s.db.GetVideosByChannel(channelID, 50)
	fetched := 0
	for _, v := range dbVideos {
		if v.Published.IsZero() && fetched < 10 {
			pubDate := s.downloader.GetVideoPublishDate(r.Context(), v.URL)
			if !pubDate.IsZero() {
				s.db.UpdateVideoPublished(v.ID, pubDate)
			}
			fetched++
		}
	}

	_ = s.db.UpdateChannelLastCheck(channelID)

	newCount, _ := s.db.CountNewVideosSince(channelID, oldLastCheck)
	_ = s.db.UpdateChannelNewVideosCount(channelID, newCount)

	s.respond(w, Response{Success: true, Data: map[string]interface{}{
		"videos":   dbVideos,
		"newCount": newCount,
	}})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	s.respond(w, Response{Success: true, Data: []interface{}{}})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	t := &db.Task{
		Title:    req.Title,
		Status:   "open",
	}

	if err := s.db.CreateTask(t); err != nil {
		s.respondError(w, "Failed to create task", http.StatusInternalServerError)
		return
	}

	s.respond(w, Response{Success: true, Data: t})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, id string) {
	var taskID int64
	if _, err := fmt.Sscanf(id, "%d", &taskID); err != nil {
		s.respondError(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	t, err := s.db.GetTask(taskID)
	if err != nil {
		s.respondError(w, "Task not found", http.StatusNotFound)
		return
	}

	s.respond(w, Response{Success: true, Data: t})
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request, id string) {
	s.respond(w, Response{Success: true})
}

func (s *Server) videoDownloadsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
			s.respondError(w, "URL is required", http.StatusBadRequest)
			return
		}
		if err := s.db.MarkVideoDownloaded(req.URL); err != nil {
			s.respondError(w, "Failed to mark video", http.StatusInternalServerError)
			return
		}
		s.respond(w, Response{Success: true})
	case http.MethodDelete:
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
			s.respondError(w, "URL is required", http.StatusBadRequest)
			return
		}
		if err := s.db.UnmarkVideoDownloaded(req.URL); err != nil {
			s.respondError(w, "Failed to unmark video", http.StatusInternalServerError)
			return
		}
		s.respond(w, Response{Success: true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) respond(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) respondError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(Response{Success: false, Error: message})
}

func extractID(path, prefix string) string {
	if len(path) > len(prefix) {
		return path[len(prefix):]
	}
	return ""
}