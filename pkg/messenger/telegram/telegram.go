package telegram

import (
	"context"
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/mitry/ytbloader/pkg/agent"
)

type Messenger struct {
	bot   *tgbotapi.BotAPI
	agent *agent.Agent
}

func New(token string, agent *agent.Agent) (*Messenger, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	m := &Messenger{
		bot:   bot,
		agent: agent,
	}

	agent.RegisterSender("telegram", m.sendFile)

	return m, nil
}

func (m *Messenger) Start(ctx context.Context) {
	log.Printf("Authorized on account %s", m.bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := m.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			if update.Message == nil {
				continue
			}

			go m.handleMessage(ctx, update.Message)
		}
	}
}

func (m *Messenger) handleMessage(ctx context.Context, message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID

	resp, err := m.agent.ProcessMessage(ctx, "telegram", userID, chatID, message.Text)
	if err != nil {
		log.Printf("Error processing message: %v", err)
		msg := tgbotapi.NewMessage(chatID, "Error processing message")
		m.bot.Send(msg)
		return
	}

	msg := tgbotapi.NewMessage(chatID, resp.Text)
	m.bot.Send(msg)

	if resp.HasFile {
		file := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(resp.FilePath))
		m.bot.Send(file)
	}
}

func (m *Messenger) sendFile(messengerID string, chatID int64, filePath string) error {
	file := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	_, err := m.bot.Send(file)
	return err
}

func (m *Messenger) Stop() {
	m.bot.StopReceivingUpdates()
}