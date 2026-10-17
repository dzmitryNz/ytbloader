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
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/mitry/ytbloader/pkg/agent"
)

const defaultBaseURL = "https://discord.com/api/v10"

// maxMessageLen is Discord's limit for the content of one message.
const maxMessageLen = 2000

// processor is the part of *agent.Agent the messenger uses.
type processor interface {
	ProcessMessage(ctx context.Context, messengerID string, userID int64, chatID int64, message string) (*agent.Response, error)
}

// outbound sends replies; the messenger itself sends them over the REST API.
type outbound interface {
	SendMessage(channelID, message string) error
	sendFile(messengerID string, chatID int64, filePath string) error
}

type Messenger struct {
	token   string
	agent   processor
	out     outbound
	client  *http.Client
	baseURL string
}

type DiscordMessage struct {
	Content string `json:"content"`
}

func New(token string, agt *agent.Agent) (*Messenger, error) {
	m := &Messenger{
		token:   token,
		agent:   agt,
		client:  &http.Client{Timeout: 30 * time.Second},
		baseURL: defaultBaseURL,
	}
	m.out = m

	agt.RegisterSender("discord", m.sendFile)

	return m, nil
}

// Start connects to the gateway and answers commands until ctx is cancelled.
func (m *Messenger) Start(ctx context.Context) error {
	session, err := discordgo.New("Bot " + m.token)
	if err != nil {
		return fmt.Errorf("failed to create Discord session: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	session.AddHandler(func(_ *discordgo.Session, e *discordgo.MessageCreate) {
		m.onMessage(ctx, e.Message)
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("failed to connect to Discord: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			log.Printf("Failed to close Discord session: %v", err)
		}
	}()
	log.Println("Discord gateway connected")

	<-ctx.Done()
	return nil
}

// onMessage passes slash commands from people to the agent. Other messages are
// ignored, so the bot stays quiet in busy channels and never answers bots.
func (m *Messenger) onMessage(ctx context.Context, msg *discordgo.Message) {
	if msg == nil || msg.Author == nil || msg.Author.Bot || !strings.HasPrefix(msg.Content, "/") {
		return
	}

	if err := m.handle(ctx, msg.Author.ID, msg.ChannelID, msg.Content); err != nil {
		log.Printf("Discord message from %s in %s: %v", msg.Author.ID, msg.ChannelID, err)
	}
}

func (m *Messenger) handle(ctx context.Context, userID, channelID, content string) error {
	uid, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid user id %q: %w", userID, err)
	}
	cid, err := strconv.ParseInt(channelID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid channel id %q: %w", channelID, err)
	}

	resp, err := m.agent.ProcessMessage(ctx, "discord", uid, cid, content)
	if err != nil {
		m.reply(channelID, "Error processing message")
		return fmt.Errorf("process message: %w", err)
	}

	m.reply(channelID, resp.Text)

	if resp.HasFile {
		if err := m.out.sendFile("discord", cid, resp.FilePath); err != nil {
			return fmt.Errorf("send file: %w", err)
		}
	}
	return nil
}

func (m *Messenger) reply(channelID, text string) {
	for _, part := range splitMessage(text, maxMessageLen) {
		if err := m.out.SendMessage(channelID, part); err != nil {
			log.Printf("Failed to send Discord message: %v", err)
			return
		}
	}
}

// splitMessage cuts text into pieces of at most limit bytes, on line
// boundaries where it can.
func splitMessage(text string, limit int) []string {
	var parts []string
	var cur string
	open := false

	for _, line := range strings.Split(text, "\n") {
		for len(line) > limit {
			if open {
				parts = append(parts, cur)
				open = false
			}
			parts = append(parts, line[:limit])
			line = line[limit:]
		}

		switch {
		case !open:
			cur, open = line, true
		case len(cur)+1+len(line) <= limit:
			cur += "\n" + line
		default:
			parts = append(parts, cur)
			cur = line
		}
	}
	if open {
		parts = append(parts, cur)
	}

	return parts
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
