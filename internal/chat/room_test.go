package chat

import (
	"sync"
	"testing"
	"time"
)

func TestClientLifecyclePersistsMessagesAndPresence(t *testing.T) {
	room, err := Open(t.TempDir(), "Team Chat")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	client, err := room.Join(Identity{UserID: "alice@example", Name: "Alice"})
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}

	if err := client.Send("hello"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := client.Rename("Alicia"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	snapshot, err := client.Snapshot(100)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Room != "team-chat" {
		t.Fatalf("snapshot.Room = %q, want team-chat", snapshot.Room)
	}
	if len(snapshot.Presence) != 1 {
		t.Fatalf("presence len = %d, want 1", len(snapshot.Presence))
	}
	if !snapshot.Presence[0].Self || snapshot.Presence[0].Name != "Alicia" {
		t.Fatalf("presence = %+v, want self Alicia", snapshot.Presence[0])
	}
	if !hasMessage(snapshot.Messages, KindChat, "hello") {
		t.Fatalf("messages missing chat hello: %+v", snapshot.Messages)
	}
	if !hasMessage(snapshot.Messages, KindRename, "Alice") {
		t.Fatalf("messages missing rename from Alice: %+v", snapshot.Messages)
	}

	if err := client.Leave(); err != nil {
		t.Fatalf("Leave() error = %v", err)
	}
	after, err := room.Snapshot(Identity{UserID: "alice@example", Name: "Alicia"}, 100)
	if err != nil {
		t.Fatalf("Snapshot() after leave error = %v", err)
	}
	if len(after.Presence) != 0 {
		t.Fatalf("presence len after leave = %d, want 0", len(after.Presence))
	}
	if !hasKind(after.Messages, KindLeave) {
		t.Fatalf("messages missing leave event: %+v", after.Messages)
	}
}

func TestConcurrentSendAppendsAllMessages(t *testing.T) {
	room, err := Open(t.TempDir(), "lobby")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	client, err := room.Join(Identity{UserID: "alice", Name: "Alice"})
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	defer client.Leave()

	const count = 20
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Send("ping"); err != nil {
				t.Errorf("Send() error = %v", err)
			}
		}()
	}
	wg.Wait()

	snapshot, err := client.Snapshot(100)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	got := 0
	for _, message := range snapshot.Messages {
		if message.Kind == KindChat && message.Body == "ping" {
			got++
		}
	}
	if got != count {
		t.Fatalf("chat messages = %d, want %d", got, count)
	}
}

func TestInferLocalIdentityUsesSSHConnection(t *testing.T) {
	t.Setenv("USER", "chat")
	t.Setenv("LOGNAME", "")
	t.Setenv("SSH_CONNECTION", "100.64.0.9 57123 100.64.0.1 22")
	t.Setenv("YAPSSH_ID", "")
	t.Setenv("YAPSSH_NAME", "")

	identity := InferLocalIdentity("", "")
	if identity.UserID != "chat@100.64.0.9" {
		t.Fatalf("UserID = %q, want chat@100.64.0.9", identity.UserID)
	}
	if identity.Name != "chat" {
		t.Fatalf("Name = %q, want chat", identity.Name)
	}
}

func TestReadPresenceDropsStaleSessions(t *testing.T) {
	room, err := Open(t.TempDir(), "lobby")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	client, err := room.Join(Identity{UserID: "alice", Name: "Alice"})
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	client.mu.Lock()
	err = client.writePresenceLocked(time.Now().Add(-2 * staleAfter))
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("write stale presence error = %v", err)
	}

	snapshot, err := client.Snapshot(100)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Presence) != 0 {
		t.Fatalf("presence len = %d, want stale entry removed", len(snapshot.Presence))
	}
}

func hasMessage(messages []Message, kind, body string) bool {
	for _, message := range messages {
		if message.Kind == kind && message.Body == body {
			return true
		}
	}
	return false
}

func hasKind(messages []Message, kind string) bool {
	for _, message := range messages {
		if message.Kind == kind {
			return true
		}
	}
	return false
}
