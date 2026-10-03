package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mitry/ytbloader/pkg/db"
)

func newTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{db: database}, database
}

func TestDeleteDownloadRemovesFile(t *testing.T) {
	s, database := newTestServer(t)
	file := filepath.Join(t.TempDir(), "video.mp3")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &db.Download{URL: "u", Status: "completed", OutputPath: file}
	if err := database.CreateDownload(d); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.deleteDownload(rec, httptest.NewRequest(http.MethodDelete, "/", nil), "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
	if _, err := database.GetDownload(d.ID); err == nil {
		t.Error("db row still exists")
	}
}

func TestDeleteDownloadMissingFileStillDeletesRow(t *testing.T) {
	s, database := newTestServer(t)
	d := &db.Download{URL: "u", Status: "completed", OutputPath: filepath.Join(t.TempDir(), "gone.mp3")}
	if err := database.CreateDownload(d); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.deleteDownload(rec, httptest.NewRequest(http.MethodDelete, "/", nil), "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := database.GetDownload(d.ID); err == nil {
		t.Error("db row still exists")
	}
}
