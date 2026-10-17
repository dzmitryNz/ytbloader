package telegram

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/mitry/ytbloader/pkg/agent"
)

type button struct {
	Text string
	Data string
}

// keyboard is an inline keyboard: rows of buttons. A nil keyboard means none.
type keyboard [][]button

// botClient is the part of the Telegram API the messenger uses.
type botClient interface {
	SendMessage(ctx context.Context, chatID int64, text string, kb keyboard) error
	SendDocument(ctx context.Context, chatID int64, path string) error
	EditMessage(ctx context.Context, chatID int64, messageID int, text string, kb keyboard) error
	AnswerCallback(ctx context.Context, callbackID string) error
}

// processor is the part of *agent.Agent the messenger uses.
type processor interface {
	ProcessMessage(ctx context.Context, messengerID string, userID int64, chatID int64, message string) (*agent.Response, error)
}

type Messenger struct {
	bot    *bot.Bot
	client botClient
	agent  processor
}

func New(token string, agt *agent.Agent) (*Messenger, error) {
	m := &Messenger{agent: agt}

	b, err := bot.New(token, bot.WithSkipGetMe(), bot.WithDefaultHandler(m.onUpdate))
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	me, err := b.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}
	log.Printf("Authorized on account %s", me.Username)

	m.bot = b
	m.client = apiClient{b}

	agt.RegisterSender("telegram", m.sendFile)

	return m, nil
}

// Start polls for updates until ctx is cancelled.
func (m *Messenger) Start(ctx context.Context) {
	m.bot.Start(ctx)
}

func (m *Messenger) onUpdate(ctx context.Context, _ *bot.Bot, update *models.Update) {
	switch {
	case update.CallbackQuery != nil:
		cq := update.CallbackQuery
		msg := cq.Message.Message
		if msg == nil {
			// The message is too old for Telegram to send it back.
			go m.answer(ctx, cq.ID)
			return
		}
		go m.handleCallback(ctx, cq.ID, cq.From.ID, msg.Chat.ID, msg.ID, cq.Data)
	case update.Message != nil && update.Message.From != nil:
		go m.handleMessage(ctx, update.Message.From.ID, update.Message.Chat.ID, update.Message.Text)
	}
}

func (m *Messenger) handleMessage(ctx context.Context, userID, chatID int64, text string) {
	resp, err := m.agent.ProcessMessage(ctx, "telegram", userID, chatID, text)
	if err != nil {
		log.Printf("Error processing message: %v", err)
		m.send(ctx, chatID, "Error processing message", nil)
		return
	}

	m.send(ctx, chatID, resp.Text, pagerKeyboard(resp.Pager))

	if resp.HasFile {
		if err := m.client.SendDocument(ctx, chatID, resp.FilePath); err != nil {
			log.Printf("Failed to send Telegram document: %v", err)
		}
	}
}

// handleCallback turns a Prev/Next press into the same command a user would
// type and shows the result in place of the pressed message.
func (m *Messenger) handleCallback(ctx context.Context, callbackID string, userID, chatID int64, messageID int, data string) {
	defer m.answer(ctx, callbackID)

	command, ok := parseCallbackData(data)
	if !ok {
		log.Printf("Ignoring unknown callback data %q", data)
		return
	}

	resp, err := m.agent.ProcessMessage(ctx, "telegram", userID, chatID, command)
	if err != nil {
		log.Printf("Error processing callback %q: %v", data, err)
		return
	}

	if err := m.client.EditMessage(ctx, chatID, messageID, resp.Text, pagerKeyboard(resp.Pager)); err != nil {
		log.Printf("Failed to edit Telegram message: %v", err)
	}
}

func (m *Messenger) answer(ctx context.Context, callbackID string) {
	if err := m.client.AnswerCallback(ctx, callbackID); err != nil {
		log.Printf("Failed to answer Telegram callback: %v", err)
	}
}

func (m *Messenger) send(ctx context.Context, chatID int64, text string, kb keyboard) {
	if err := m.client.SendMessage(ctx, chatID, text, kb); err != nil {
		log.Printf("Failed to send Telegram message: %v", err)
	}
}

func (m *Messenger) sendFile(messengerID string, chatID int64, filePath string) error {
	return m.client.SendDocument(context.Background(), chatID, filePath)
}

// pagerKeyboard returns the Prev/Next row for a paged response, or nil.
// Callback data: "p:subs:<page>" and "p:videos:<channelID>:<page>".
func pagerKeyboard(p *agent.Pager) keyboard {
	if p == nil {
		return nil
	}

	data := func(page int) string {
		if p.Kind == "videos" {
			return fmt.Sprintf("p:videos:%d:%d", p.ChannelID, page)
		}
		return fmt.Sprintf("p:%s:%d", p.Kind, page)
	}

	var row []button
	if p.Page > 1 {
		row = append(row, button{Text: "« Prev", Data: data(p.Page - 1)})
	}
	if p.Page < p.TotalPages {
		row = append(row, button{Text: "Next »", Data: data(p.Page + 1)})
	}
	if len(row) == 0 {
		return nil
	}
	return keyboard{row}
}

// parseCallbackData maps button data back to an agent command.
func parseCallbackData(data string) (string, bool) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 || parts[0] != "p" {
		return "", false
	}

	switch {
	case parts[1] == "subs" && len(parts) == 3:
		page, ok := parsePositive(parts[2])
		if !ok {
			return "", false
		}
		return fmt.Sprintf("/subs %d", page), true
	case parts[1] == "videos" && len(parts) == 4:
		id, idOK := parsePositive(parts[2])
		page, pageOK := parsePositive(parts[3])
		if !idOK || !pageOK {
			return "", false
		}
		return fmt.Sprintf("/videos %d %d", id, page), true
	}
	return "", false
}

func parsePositive(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// apiClient adapts *bot.Bot to botClient.
type apiClient struct {
	b *bot.Bot
}

func (c apiClient) SendMessage(ctx context.Context, chatID int64, text string, kb keyboard) error {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text}
	if markup := inlineMarkup(kb); markup != nil {
		params.ReplyMarkup = markup
	}
	_, err := c.b.SendMessage(ctx, params)
	return err
}

func (c apiClient) SendDocument(ctx context.Context, chatID int64, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	_, err = c.b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filepath.Base(path), Data: f},
	})
	return err
}

func (c apiClient) EditMessage(ctx context.Context, chatID int64, messageID int, text string, kb keyboard) error {
	params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: text}
	if markup := inlineMarkup(kb); markup != nil {
		params.ReplyMarkup = markup
	}
	_, err := c.b.EditMessageText(ctx, params)
	return err
}

func (c apiClient) AnswerCallback(ctx context.Context, callbackID string) error {
	_, err := c.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID})
	return err
}

func inlineMarkup(kb keyboard) *models.InlineKeyboardMarkup {
	if len(kb) == 0 {
		return nil
	}
	markup := &models.InlineKeyboardMarkup{}
	for _, row := range kb {
		var buttons []models.InlineKeyboardButton
		for _, b := range row {
			buttons = append(buttons, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		markup.InlineKeyboard = append(markup.InlineKeyboard, buttons)
	}
	return markup
}
