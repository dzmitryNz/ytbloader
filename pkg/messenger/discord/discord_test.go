package discord

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSendFileUploadsMultipart(t *testing.T) {
	var (
		requests int
		path     string
		auth     string
		fileName string
		fileData []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			return
		}
		f, hdr, err := r.FormFile("files[0]")
		if err != nil {
			t.Errorf("FormFile(files[0]): %v", err)
			return
		}
		defer f.Close()
		fileName = hdr.Filename
		fileData, _ = io.ReadAll(f)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	filePath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(filePath, []byte("video-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := &Messenger{token: "tok", client: srv.Client(), baseURL: srv.URL}
	if err := m.sendFile("discord", 123456789012345678, filePath); err != nil {
		t.Fatalf("sendFile: %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if path != "/channels/123456789012345678/messages" {
		t.Errorf("path = %q", path)
	}
	if auth != "Bot tok" {
		t.Errorf("Authorization = %q, want %q", auth, "Bot tok")
	}
	if fileName != "video.mp4" {
		t.Errorf("file name = %q, want video.mp4", fileName)
	}
	if string(fileData) != "video-bytes" {
		t.Errorf("file content = %q", fileData)
	}
}

func TestSendFileReturnsErrorOnFailureStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	filePath := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := &Messenger{token: "tok", client: srv.Client(), baseURL: srv.URL}
	if err := m.sendFile("discord", 1, filePath); err == nil {
		t.Fatal("expected an error for a 403 response")
	}
}
