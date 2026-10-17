package mattermost

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSendFileUploadsThenPosts(t *testing.T) {
	var (
		calls    []string
		auth     string
		query    string
		fileName string
		fileData []byte
		post     MattermostPost
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/v4/files":
			query = r.URL.Query().Get("channel_id")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("ParseMultipartForm: %v", err)
				return
			}
			f, hdr, err := r.FormFile("files")
			if err != nil {
				t.Errorf("FormFile(files): %v", err)
				return
			}
			defer f.Close()
			fileName = hdr.Filename
			fileData, _ = io.ReadAll(f)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"file_infos":[{"id":"file123"}]}`)
		case "/api/v4/posts":
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &post); err != nil {
				t.Errorf("post body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	filePath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(filePath, []byte("video-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := &Messenger{url: srv.URL, token: "tok", client: srv.Client()}
	if err := m.sendFile("mattermost", 42, filePath); err != nil {
		t.Fatalf("sendFile: %v", err)
	}

	if len(calls) != 2 || calls[0] != "POST /api/v4/files" || calls[1] != "POST /api/v4/posts" {
		t.Fatalf("calls = %v, want upload then post", calls)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}
	if query != "42" {
		t.Errorf("channel_id query = %q, want 42", query)
	}
	if fileName != "video.mp4" || string(fileData) != "video-bytes" {
		t.Errorf("uploaded %q / %q", fileName, fileData)
	}
	if post.ChannelId != "42" {
		t.Errorf("post channel = %q, want 42", post.ChannelId)
	}
	if len(post.FileIds) != 1 || post.FileIds[0] != "file123" {
		t.Errorf("file_ids = %v, want [file123]", post.FileIds)
	}
}
