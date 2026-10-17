package discord

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

const defaultBaseURL = "https://discord.com/api/v10"

type Messenger struct {
	token   string
	agent   *agent.Agent
	client  *http.Client
	baseURL string
}

type DiscordMessage struct {
	Content string `json:"content"`
}

func New(token string, agent *agent.Agent) (*Messenger, error) {
	m := &Messenger{
		token:   token,
		agent:   agent,
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: defaultBaseURL,
	}

	agent.RegisterSender("discord", m.sendFile)

	return m, nil
}

func (m *Messenger) Start(ctx context.Context) error {
	log.Println("Discord bot started")

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			time.Sleep(1 * time.Second)
		}
	}
}

func (m *Messenger) SendMessage(channelID, message string) error {
	msg := DiscordMessage{Content: message}
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/channels/%s/messages", m.baseURL, channelID), bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send message: %s", string(body))
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
	payload, err := w.CreateFormFile("files[0]", filepath.Base(filePath))
	if err != nil {
		return err
	}

	if _, err := io.Copy(payload, file); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/channels/%d/messages", m.baseURL, chatID), &buf)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bot "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send file: %s", string(respBody))
	}

	return nil
}

func (m *Messenger) Stop() {
	log.Println("Discord bot stopped")
}
