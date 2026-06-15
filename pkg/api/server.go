package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/mitry/ytbloader/pkg/db"
	"github.com/mitry/ytbloader/pkg/downloader"
)

type Server struct {
	server   *http.Server
	db       *db.DB
	downloader *downloader.Downloader
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

func NewServer(addr string, database *db.DB, dl *downloader.Downloader) *Server {
	s := &Server{
		db:       database,
		downloader: dl,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.healthHandler)
	mux.HandleFunc("/api/downloads", s.downloadsHandler)
	mux.HandleFunc("/api/downloads/", s.downloadHandler)
	mux.HandleFunc("/api/tasks", s.tasksHandler)
	mux.HandleFunc("/api/tasks/", s.taskHandler)

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

	switch r.Method {
	case http.MethodGet:
		s.getDownload(w, r, id)
	case http.MethodPut:
		s.updateDownload(w, r, id)
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
	s.respond(w, Response{Success: true, Data: []interface{}{}})
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

func (s *Server) updateDownload(w http.ResponseWriter, r *http.Request, id string) {
	s.respond(w, Response{Success: true})
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