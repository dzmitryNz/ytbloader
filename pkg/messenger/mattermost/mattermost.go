package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mitry/ytbloader/pkg/agent"
)

type Messenger struct {
	url    string
	token  string
	agent  *agent.Agent
	client *http.Client
}

type MattermostPost struct {
	ChannelId string   `json:"channel_id"`
	Message   string   `json:"message"`
	RootId    string   `json:"root_id,omitempty"`
	FileIds   []string `json:"file_ids,omitempty"`
}

func New(url, token string, agent *agent.Agent) (*Messenger, error) {
	m := &Messenger{
		url:    url,
		token:  token,
		agent:  agent,
		client: &http.Client{Timeout: 30 * time.Second},
	}

	agent.RegisterSender("mattermost", m.sendFile)

	return m, nil
}

// Start only logs a notice: Mattermost is send-only (files for downloads
// requested elsewhere), the bot does not read posts.
func (m *Messenger) Start(ctx context.Context) error {
	log.Println("mattermost: receiving is not supported")

	<-ctx.Done()
	return nil
}

func (m *Messenger) SendPost(channelID, message string) error {
	return m.createPost(MattermostPost{
		ChannelId: channelID,
		Message:   message,
	})
}

func (m *Messenger) createPost(post MattermostPost) error {
	jsonData, err := json.Marshal(post)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v4/posts", m.url), bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send post: %s", string(body))
	}

	return nil
}

func (m *Messenger) sendFile(messengerID string, chatID int64, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	payload, err := w.CreateFormFile("files", filepath.Base(filePath))
	if err != nil {
		return err
	}

	if _, err := io.Copy(payload, file); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	channelId := fmt.Sprintf("%d", chatID)
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v4/files?channel_id=%s", m.url, channelId), &buf)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload file: %s", string(respBody))
	}

	var uploaded struct {
		FileInfos []struct {
			ID string `json:"id"`
		} `json:"file_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		return fmt.Errorf("failed to decode upload response: %w", err)
	}
	if len(uploaded.FileInfos) == 0 {
		return fmt.Errorf("upload response has no file ids")
	}

	post := MattermostPost{ChannelId: channelId}
	for _, info := range uploaded.FileInfos {
		post.FileIds = append(post.FileIds, info.ID)
	}
	return m.createPost(post)
}

func (m *Messenger) Stop() {
	log.Println("Mattermost bot stopped")
}
