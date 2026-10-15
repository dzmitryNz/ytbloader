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

func TestDeleteSubscriptionWithVideos(t *testing.T) {
	s, database := newTestServer(t)
	c := &db.Channel{URL: "https://youtube.com/@x", Name: "x", MessengerID: "web", UserID: 1}
	if err := database.CreateChannel(c); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertVideo(&db.Video{ChannelID: c.ID, URL: "v1", Title: "t"}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.deleteSubscription(rec, httptest.NewRequest(http.MethodDelete, "/", nil), "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := database.GetChannel(c.ID); err == nil {
		t.Error("channel still exists")
	}
	videos, err := database.GetVideosByChannel(c.ID, 10)
	if err != nil || len(videos) != 0 {
		t.Errorf("videos left: %d, err %v", len(videos), err)
	}
}
