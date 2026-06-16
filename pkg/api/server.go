package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

	d := &db.Download{
		URL:    req.URL,
		Status: "pending",
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