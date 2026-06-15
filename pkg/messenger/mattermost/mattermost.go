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
	ChannelId string `json:"channel_id"`
	Message   string `json:"message"`
	RootId    string `json:"root_id,omitempty"`
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

func (m *Messenger) Start(ctx context.Context) error {
	log.Println("Mattermost bot started")

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			time.Sleep(1 * time.Second)
		}
	}
}

func (m *Messenger) SendPost(channelID, message string) error {
	post := MattermostPost{
		ChannelId: channelID,
		Message:   message,
	}

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

	if resp.StatusCode != http.StatusOK {
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

	body := &multipart.Writer{}
	payload, err := body.CreateFormFile("files", filePath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(payload, file); err != nil {
		return err
	}

 channelId := fmt.Sprintf("%d", chatID)
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v4/files/upload?channel_id=%s", m.url, channelId), nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", body.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload file: %s", string(respBody))
	}

	return nil
}

func (m *Messenger) Stop() {
	log.Println("Mattermost bot stopped")
}