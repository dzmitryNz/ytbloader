package telegram

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mitry/ytbloader/pkg/agent"
)

type sentMessage struct {
	chatID int64
	text   string
	kb     keyboard
}

type editedMessage struct {
	chatID    int64
	messageID int
	text      string
	kb        keyboard
}

type fakeClient struct {
	messages  []sentMessage
	documents []string
	edits     []editedMessage
	answers   []string
}

func (f *fakeClient) SendMessage(ctx context.Context, chatID int64, text string, kb keyboard) error {
	f.messages = append(f.messages, sentMessage{chatID, text, kb})
	return nil
}

func (f *fakeClient) SendDocument(ctx context.Context, chatID int64, path string) error {
	f.documents = append(f.documents, path)
	return nil
}

func (f *fakeClient) EditMessage(ctx context.Context, chatID int64, messageID int, text string, kb keyboard) error {
	f.edits = append(f.edits, editedMessage{chatID, messageID, text, kb})
	return nil
}

func (f *fakeClient) AnswerCallback(ctx context.Context, callbackID string) error {
	f.answers = append(f.answers, callbackID)
	return nil
}

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

func newTestMessenger(resp *agent.Response, err error) (*Messenger, *fakeClient, *fakeProcessor) {
	client := &fakeClient{}
	proc := &fakeProcessor{resp: resp, err: err}
	return &Messenger{client: client, agent: proc}, client, proc
}

func TestHandleMessageRepliesWithText(t *testing.T) {
	m, client, proc := newTestMessenger(&agent.Response{Text: "hello"}, nil)

	m.handleMessage(context.Background(), 7, 42, "/help")

	if want := []processCall{{"telegram", 7, 42, "/help"}}; !reflect.DeepEqual(proc.calls, want) {
		t.Errorf("agent calls = %+v, want %+v", proc.calls, want)
	}
	if want := []sentMessage{{42, "hello", nil}}; !reflect.DeepEqual(client.messages, want) {
		t.Errorf("messages = %+v, want %+v", client.messages, want)
	}
	if len(client.documents) != 0 {
		t.Errorf("documents = %v, want none", client.documents)
	}
}

func TestHandleMessageSendsFileAfterText(t *testing.T) {
	m, client, _ := newTestMessenger(&agent.Response{Text: "done", HasFile: true, FilePath: "/tmp/a.mp3"}, nil)

	m.handleMessage(context.Background(), 7, 42, "/dl x --send")

	if len(client.messages) != 1 || client.messages[0].text != "done" {
		t.Errorf("messages = %+v, want one with text %q", client.messages, "done")
	}
	if want := []string{"/tmp/a.mp3"}; !reflect.DeepEqual(client.documents, want) {
		t.Errorf("documents = %v, want %v", client.documents, want)
	}
}

func TestHandleMessageAgentErrorRepliesGenericText(t *testing.T) {
	m, client, _ := newTestMessenger(nil, errors.New("boom"))

	m.handleMessage(context.Background(), 7, 42, "/dl x")

	want := []sentMessage{{42, "Error processing message", nil}}
	if !reflect.DeepEqual(client.messages, want) {
		t.Errorf("messages = %+v, want %+v", client.messages, want)
	}
}

func TestHandleMessageAddsKeyboardForPagedResponse(t *testing.T) {
	tests := []struct {
		name  string
		pager agent.Pager
		want  keyboard
	}{
		{
			name:  "first page has only Next",
			pager: agent.Pager{Kind: "subs", Page: 1, TotalPages: 3},
			want:  keyboard{{{Text: "Next »", Data: "p:subs:2"}}},
		},
		{
			name:  "middle page has Prev and Next",
			pager: agent.Pager{Kind: "subs", Page: 2, TotalPages: 3},
			want:  keyboard{{{Text: "« Prev", Data: "p:subs:1"}, {Text: "Next »", Data: "p:subs:3"}}},
		},
		{
			name:  "last page has only Prev",
			pager: agent.Pager{Kind: "subs", Page: 3, TotalPages: 3},
			want:  keyboard{{{Text: "« Prev", Data: "p:subs:2"}}},
		},
		{
			name:  "videos carry the channel id",
			pager: agent.Pager{Kind: "videos", ChannelID: 12, Page: 1, TotalPages: 2},
			want:  keyboard{{{Text: "Next »", Data: "p:videos:12:2"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, client, _ := newTestMessenger(&agent.Response{Text: "list", Pager: &tt.pager}, nil)

			m.handleMessage(context.Background(), 7, 42, "/subs")

			if len(client.messages) != 1 || !reflect.DeepEqual(client.messages[0].kb, tt.want) {
				t.Errorf("messages = %+v, want keyboard %+v", client.messages, tt.want)
			}
		})
	}
}

func TestHandleCallbackEditsMessageWithRequestedPage(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		wantCommand string
	}{
		{"subs", "p:subs:2", "/subs 2"},
		{"videos", "p:videos:12:3", "/videos 12 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pager := &agent.Pager{Kind: "subs", Page: 2, TotalPages: 3}
			m, client, proc := newTestMessenger(&agent.Response{Text: "page text", Pager: pager}, nil)

			m.handleCallback(context.Background(), "cb1", 7, 42, 99, tt.data)

			if want := []processCall{{"telegram", 7, 42, tt.wantCommand}}; !reflect.DeepEqual(proc.calls, want) {
				t.Errorf("agent calls = %+v, want %+v", proc.calls, want)
			}
			if len(client.edits) != 1 {
				t.Fatalf("edits = %+v, want one", client.edits)
			}
			e := client.edits[0]
			if e.chatID != 42 || e.messageID != 99 || e.text != "page text" {
				t.Errorf("edit = %+v, want chat 42, message 99, text %q", e, "page text")
			}
			if len(e.kb) != 1 || len(e.kb[0]) != 2 {
				t.Errorf("edit keyboard = %+v, want Prev and Next", e.kb)
			}
			if len(client.messages) != 0 {
				t.Errorf("messages = %+v, a press must edit, not send", client.messages)
			}
			if want := []string{"cb1"}; !reflect.DeepEqual(client.answers, want) {
				t.Errorf("answers = %v, want %v", client.answers, want)
			}
		})
	}
}

func TestHandleCallbackIgnoresMalformedData(t *testing.T) {
	for _, data := range []string{"", "x", "p:subs", "p:subs:x", "p:subs:0", "p:videos:12", "p:videos:x:2", "p:other:1", "p:subs:2:3"} {
		t.Run(data, func(t *testing.T) {
			m, client, proc := newTestMessenger(&agent.Response{Text: "x"}, nil)

			m.handleCallback(context.Background(), "cb1", 7, 42, 99, data)

			if len(proc.calls) != 0 {
				t.Errorf("agent calls = %+v, want none", proc.calls)
			}
			if len(client.edits) != 0 {
				t.Errorf("edits = %+v, want none", client.edits)
			}
			if want := []string{"cb1"}; !reflect.DeepEqual(client.answers, want) {
				t.Errorf("answers = %v, want %v (the button spinner must stop)", client.answers, want)
			}
		})
	}
}

func TestHandleCallbackAgentErrorKeepsMessage(t *testing.T) {
	m, client, _ := newTestMessenger(nil, errors.New("boom"))

	m.handleCallback(context.Background(), "cb1", 7, 42, 99, "p:subs:2")

	if len(client.edits) != 0 {
		t.Errorf("edits = %+v, want none", client.edits)
	}
	if len(client.answers) != 1 {
		t.Errorf("answers = %v, want one", client.answers)
	}
}

func TestSendFileUsesDocument(t *testing.T) {
	m, client, _ := newTestMessenger(nil, nil)

	if err := m.sendFile("telegram", 42, "/tmp/a.mp3"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/tmp/a.mp3"}; !reflect.DeepEqual(client.documents, want) {
		t.Errorf("documents = %v, want %v", client.documents, want)
	}
}
