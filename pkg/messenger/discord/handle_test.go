package discord

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/mitry/ytbloader/pkg/agent"
)

type processCall struct {
	messengerID string
	userID      int64
	chatID      int64
	message     string
}

type fakeProcessor struct {
	calls []processCall
	resp  *agent.Response
	err   error
}

func (p *fakeProcessor) ProcessMessage(ctx context.Context, messengerID string, userID, chatID int64, message string) (*agent.Response, error) {
	p.calls = append(p.calls, processCall{messengerID, userID, chatID, message})
	return p.resp, p.err
}

type sentText struct {
	channelID string
	text      string
}

type sentFile struct {
	chatID int64
	path   string
}

type fakeOutbound struct {
	texts []sentText
	files []sentFile
}

func (o *fakeOutbound) SendMessage(channelID, text string) error {
	o.texts = append(o.texts, sentText{channelID, text})
	return nil
}

func (o *fakeOutbound) sendFile(messengerID string, chatID int64, filePath string) error {
	o.files = append(o.files, sentFile{chatID, filePath})
	return nil
}

func newHandleMessenger(resp *agent.Response, err error) (*Messenger, *fakeProcessor, *fakeOutbound) {
	proc := &fakeProcessor{resp: resp, err: err}
	out := &fakeOutbound{}
	return &Messenger{agent: proc, out: out}, proc, out
}

func TestHandleRepliesWithText(t *testing.T) {
	m, proc, out := newHandleMessenger(&agent.Response{Text: "hello"}, nil)

	if err := m.handle(context.Background(), "111", "222", "/help"); err != nil {
		t.Fatal(err)
	}

	if want := []processCall{{"discord", 111, 222, "/help"}}; !reflect.DeepEqual(proc.calls, want) {
		t.Errorf("agent calls = %+v, want %+v", proc.calls, want)
	}
	if want := []sentText{{"222", "hello"}}; !reflect.DeepEqual(out.texts, want) {
		t.Errorf("texts = %+v, want %+v", out.texts, want)
	}
	if len(out.files) != 0 {
		t.Errorf("files = %+v, want none", out.files)
	}
}

func TestHandleSendsFileAfterText(t *testing.T) {
	m, _, out := newHandleMessenger(&agent.Response{Text: "done", HasFile: true, FilePath: "/tmp/a.mp3"}, nil)

	if err := m.handle(context.Background(), "111", "222", "/dl x --send"); err != nil {
		t.Fatal(err)
	}

	if len(out.texts) != 1 || out.texts[0].text != "done" {
		t.Errorf("texts = %+v, want one with %q", out.texts, "done")
	}
	if want := []sentFile{{222, "/tmp/a.mp3"}}; !reflect.DeepEqual(out.files, want) {
		t.Errorf("files = %+v, want %+v", out.files, want)
	}
}

func TestHandleAgentErrorRepliesGenericText(t *testing.T) {
	m, _, out := newHandleMessenger(nil, errors.New("boom"))

	if err := m.handle(context.Background(), "111", "222", "/dl x"); err == nil {
		t.Error("handle returned nil, want the agent error for logging")
	}

	if want := []sentText{{"222", "Error processing message"}}; !reflect.DeepEqual(out.texts, want) {
		t.Errorf("texts = %+v, want %+v", out.texts, want)
	}
}

func TestHandleRejectsNonNumericIDs(t *testing.T) {
	for _, ids := range [][2]string{{"abc", "222"}, {"111", "xyz"}, {"", "222"}} {
		m, proc, out := newHandleMessenger(&agent.Response{Text: "x"}, nil)

		if err := m.handle(context.Background(), ids[0], ids[1], "/help"); err == nil {
			t.Errorf("handle(%q, %q) returned nil, want an error", ids[0], ids[1])
		}
		if len(proc.calls) != 0 || len(out.texts) != 0 {
			t.Errorf("handle(%q, %q): calls = %+v, texts = %+v, want none", ids[0], ids[1], proc.calls, out.texts)
		}
	}
}

func TestHandleSplitsLongReplyUnderDiscordLimit(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, strings.Repeat("x", 99))
	}
	text := strings.Join(lines, "\n")
	m, _, out := newHandleMessenger(&agent.Response{Text: text}, nil)

	if err := m.handle(context.Background(), "111", "222", "/videos 1"); err != nil {
		t.Fatal(err)
	}

	if len(out.texts) < 2 {
		t.Fatalf("texts = %d, want the reply split in several messages", len(out.texts))
	}
	var parts []string
	for _, s := range out.texts {
		if len(s.text) > maxMessageLen {
			t.Errorf("message of %d bytes, limit %d", len(s.text), maxMessageLen)
		}
		parts = append(parts, s.text)
	}
	if got := strings.Join(parts, "\n"); got != text {
		t.Error("split messages do not add up to the original text")
	}
}

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"short text stays whole", "abc", 10, []string{"abc"}},
		{"splits on line boundaries", "aaa\nbbb\nccc", 7, []string{"aaa\nbbb", "ccc"}},
		{"a line longer than the limit is cut", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"empty text gives one empty message", "", 10, []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitMessage(tt.text, tt.limit); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitMessage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOnMessageHandlesCommandsFromPeople(t *testing.T) {
	m, proc, out := newHandleMessenger(&agent.Response{Text: "help text"}, nil)

	m.onMessage(context.Background(), &discordgo.Message{
		Author: &discordgo.User{ID: "111"}, ChannelID: "222", Content: "/help",
	})

	if want := []processCall{{"discord", 111, 222, "/help"}}; !reflect.DeepEqual(proc.calls, want) {
		t.Errorf("agent calls = %+v, want %+v", proc.calls, want)
	}
	if len(out.texts) != 1 {
		t.Errorf("texts = %+v, want one reply", out.texts)
	}
}

func TestOnMessageIgnoresBotsAndChatter(t *testing.T) {
	tests := []struct {
		name string
		msg  *discordgo.Message
	}{
		{"bot author", &discordgo.Message{Author: &discordgo.User{ID: "1", Bot: true}, ChannelID: "2", Content: "/help"}},
		{"plain chat is not a command", &discordgo.Message{Author: &discordgo.User{ID: "1"}, ChannelID: "2", Content: "hello there"}},
		{"empty content", &discordgo.Message{Author: &discordgo.User{ID: "1"}, ChannelID: "2"}},
		{"no author", &discordgo.Message{ChannelID: "2", Content: "/help"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, proc, out := newHandleMessenger(&agent.Response{Text: "x"}, nil)

			m.onMessage(context.Background(), tt.msg)

			if len(proc.calls) != 0 || len(out.texts) != 0 {
				t.Errorf("calls = %+v, texts = %+v, want none", proc.calls, out.texts)
			}
		})
	}
}
