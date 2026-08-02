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
	"time"

	"github.com/mitry/ytbloader/pkg/agent"
)

type Messenger struct {
	token  string
	agent  *agent.Agent
	client *http.Client
}

type DiscordMessage struct {
	Content string `json:"content"`
}

func New(token string, agent *agent.Agent) (*Messenger, error) {
	m := &Messenger{
		token:  token,
		agent:  agent,
		client: &http.Client{Timeout: 30 * time.Second},
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

	req, err := http.NewRequest("POST", fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", channelID), bytes.NewBuffer(jsonData))
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

	body := &multipart.Writer{}
	payload, err := body.CreateFormFile("file", filePath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(payload, file); err != nil {
		return err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("https://discord.com/api/v10/channels/%d/messages", chatID), nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", body.FormDataContentType())
	req.Header.Set("Authorization", "Bot "+m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send file: %s", string(respBody))
	}

	return nil
}

func (m *Messenger) Stop() {
	log.Println("Discord bot stopped")
}
