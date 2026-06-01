package chat

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	KindChat   = "chat"
	KindJoin   = "join"
	KindLeave  = "leave"
	KindRename = "rename"
	KindAction = "action"

	DefaultRoomName = "lobby"

	maxBodyRunes = 4000
	staleAfter   = 45 * time.Second
)

// Message is a persisted room event.
type Message struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	UserID string    `json:"user_id"`
	Name   string    `json:"name"`
	Body   string    `json:"body,omitempty"`
	SentAt time.Time `json:"sent_at"`
}

// Presence is the live view of a user in the room.
type Presence struct {
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
	Typing   bool      `json:"typing,omitempty"`
	Sessions int       `json:"sessions"`
	Self     bool      `json:"self,omitempty"`
}

// Snapshot contains everything the TUI needs to render a frame.
type Snapshot struct {
	Room     string
	Self     Identity
	Messages []Message
	Presence []Presence
}

type profile struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

type presenceFile struct {
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	Name      string    `json:"name"`
	LastSeen  time.Time `json:"last_seen"`
	Typing    bool      `json:"typing,omitempty"`
}

// Room is a single file-backed chat room.
type Room struct {
	root string
	name string
}

// Client is one connected TUI session.
type Client struct {
	room      *Room
	sessionID string

	mu     sync.Mutex
	userID string
	name   string
	typing bool
	closed bool
}

// DefaultRoot returns the default writable state directory.
func DefaultRoot() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Join(xdg, "yapssh")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "yapssh")
	}
	return ".yapssh"
}

// Open creates or opens a named room under root.
func Open(root, roomName string) (*Room, error) {
	if strings.TrimSpace(root) == "" {
		root = DefaultRoot()
	}
	name := NormalizeRoomName(roomName)
	room := &Room{
		root: filepath.Join(root, name),
		name: name,
	}
	if err := room.ensure(); err != nil {
		return nil, err
	}
	return room, nil
}

func NormalizeRoomName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return DefaultRoomName
	}
	var b strings.Builder
	dashed := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dashed = false
		case r == '-' || r == '_':
			if !dashed && b.Len() > 0 {
				b.WriteRune(r)
				dashed = true
			}
		default:
			if !dashed && b.Len() > 0 {
				b.WriteByte('-')
				dashed = true
			}
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return DefaultRoomName
	}
	return out
}

func NormalizeName(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return "guest"
	}
	runes := []rune(value)
	if len(runes) > 32 {
		value = string(runes[:32])
	}
	return value
}

func NormalizeBody(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxBodyRunes {
		value = string(runes[:maxBodyRunes])
	}
	return value
}

func (r *Room) Name() string {
	return r.name
}

func (r *Room) Root() string {
	return r.root
}

func (r *Room) Join(identity Identity) (*Client, error) {
	if err := r.ensure(); err != nil {
		return nil, err
	}
	identity.UserID = strings.TrimSpace(identity.UserID)
	if identity.UserID == "" {
		identity.UserID = "guest-" + randomHex(4)
	}

	var name string
	err := r.withLock("profiles.lock", func() error {
		profiles, err := r.readProfiles()
		if err != nil {
			return err
		}
		stored := profiles[identity.UserID]
		if stored.Name != "" {
			name = stored.Name
		} else {
			name = NormalizeName(identity.Name)
			profiles[identity.UserID] = profile{Name: name, UpdatedAt: time.Now().UTC()}
			if err := r.writeProfiles(profiles); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	client := &Client{
		room:      r,
		sessionID: newID(),
		userID:    identity.UserID,
		name:      name,
	}
	if err := client.writePresenceLocked(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := r.appendMessage(Message{
		Kind:   KindJoin,
		UserID: client.userID,
		Name:   client.name,
	}); err != nil {
		_ = client.removePresence()
		return nil, err
	}
	return client, nil
}

func (c *Client) Identity() Identity {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Identity{UserID: c.userID, Name: c.name}
}

func (c *Client) Snapshot(limit int) (Snapshot, error) {
	c.mu.Lock()
	self := Identity{UserID: c.userID, Name: c.name}
	c.mu.Unlock()
	return c.room.Snapshot(self, limit)
}

func (r *Room) Snapshot(self Identity, limit int) (Snapshot, error) {
	if err := r.ensure(); err != nil {
		return Snapshot{}, err
	}
	messages, err := r.readMessages(limit)
	if err != nil {
		return Snapshot{}, err
	}
	presence, err := r.readPresence(self.UserID)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Room:     r.name,
		Self:     self,
		Messages: messages,
		Presence: presence,
	}, nil
}

func (c *Client) Send(body string) error {
	body = NormalizeBody(body)
	if body == "" {
		return nil
	}
	c.mu.Lock()
	msg := Message{
		Kind:   KindChat,
		UserID: c.userID,
		Name:   c.name,
		Body:   body,
	}
	c.typing = false
	err := c.writePresenceLocked(time.Now().UTC())
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.room.appendMessage(msg)
}

func (c *Client) SendAction(body string) error {
	body = NormalizeBody(body)
	if body == "" {
		return nil
	}
	c.mu.Lock()
	msg := Message{
		Kind:   KindAction,
		UserID: c.userID,
		Name:   c.name,
		Body:   body,
	}
	c.typing = false
	err := c.writePresenceLocked(time.Now().UTC())
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.room.appendMessage(msg)
}

func (c *Client) Rename(name string) error {
	name = NormalizeName(name)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	oldName := c.name
	userID := c.userID
	c.name = name
	c.typing = false
	err := c.writePresenceLocked(time.Now().UTC())
	c.mu.Unlock()
	if err != nil {
		return err
	}

	if err := c.room.withLock("profiles.lock", func() error {
		profiles, err := c.room.readProfiles()
		if err != nil {
			return err
		}
		profiles[userID] = profile{Name: name, UpdatedAt: time.Now().UTC()}
		return c.room.writeProfiles(profiles)
	}); err != nil {
		return err
	}

	if oldName == name {
		return nil
	}
	return c.room.appendMessage(Message{
		Kind:   KindRename,
		UserID: userID,
		Name:   name,
		Body:   oldName,
	})
}

func (c *Client) SetTyping(active bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.typing == active {
		return nil
	}
	c.typing = active
	return c.writePresenceLocked(time.Now().UTC())
}

func (c *Client) Touch() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	return c.writePresenceLocked(time.Now().UTC())
}

func (c *Client) StartHeartbeat(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.Touch()
			}
		}
	}()
}

func (c *Client) Leave() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	msg := Message{
		Kind:   KindLeave,
		UserID: c.userID,
		Name:   c.name,
	}
	err := c.removePresenceLocked()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.room.appendMessage(msg)
}

func (c *Client) writePresenceLocked(now time.Time) error {
	if c.closed {
		return nil
	}
	entry := presenceFile{
		UserID:    c.userID,
		SessionID: c.sessionID,
		Name:      c.name,
		LastSeen:  now,
		Typing:    c.typing,
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(c.room.presencePath(c.sessionID), append(data, '\n'), 0o660)
}

func (c *Client) removePresence() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removePresenceLocked()
}

func (c *Client) removePresenceLocked() error {
	err := os.Remove(c.room.presencePath(c.sessionID))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r *Room) appendMessage(message Message) error {
	message.ID = firstNonEmpty(message.ID, newID())
	message.Kind = firstNonEmpty(message.Kind, KindChat)
	message.Name = NormalizeName(message.Name)
	message.Body = NormalizeBody(message.Body)
	message.SentAt = time.Now().UTC()

	return r.withLock("messages.lock", func() error {
		file, err := os.OpenFile(r.messagesPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
		if err != nil {
			return err
		}
		defer file.Close()
		enc := json.NewEncoder(file)
		return enc.Encode(message)
	})
}

func (r *Room) readMessages(limit int) ([]Message, error) {
	file, err := os.Open(r.messagesPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var messages []Message
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			return nil, fmt.Errorf("read messages: %w", err)
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}
	return messages, nil
}

func (r *Room) readPresence(selfID string) ([]Presence, error) {
	entries, err := os.ReadDir(r.presenceDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	byUser := make(map[string]Presence)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.presenceDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		var item presenceFile
		if err := json.Unmarshal(data, &item); err != nil {
			continue
		}
		if item.UserID == "" || now.Sub(item.LastSeen) > staleAfter {
			_ = os.Remove(path)
			continue
		}
		current := byUser[item.UserID]
		if current.UserID == "" || item.LastSeen.After(current.LastSeen) {
			current.UserID = item.UserID
			current.Name = NormalizeName(item.Name)
			current.LastSeen = item.LastSeen
		}
		current.Sessions++
		current.Typing = current.Typing || item.Typing
		current.Self = item.UserID == selfID
		byUser[item.UserID] = current
	}
	out := make([]Presence, 0, len(byUser))
	for _, item := range byUser {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Self != out[j].Self {
			return out[i].Self
		}
		left := strings.ToLower(out[i].Name)
		right := strings.ToLower(out[j].Name)
		if left == right {
			return out[i].UserID < out[j].UserID
		}
		return left < right
	})
	return out, nil
}

func (r *Room) readProfiles() (map[string]profile, error) {
	data, err := os.ReadFile(r.profilesPath())
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]profile), nil
	}
	if err != nil {
		return nil, err
	}
	var profiles map[string]profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, err
	}
	if profiles == nil {
		profiles = make(map[string]profile)
	}
	return profiles, nil
}

func (r *Room) writeProfiles(profiles map[string]profile) error {
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(r.profilesPath(), append(data, '\n'), 0o660)
}

func (r *Room) ensure() error {
	if err := os.MkdirAll(r.root, 0o770); err != nil {
		return err
	}
	return os.MkdirAll(r.presenceDir(), 0o770)
}

func (r *Room) withLock(name string, fn func() error) error {
	if err := r.ensure(); err != nil {
		return err
	}
	lockPath := filepath.Join(r.root, name)
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn()
}

func (r *Room) messagesPath() string {
	return filepath.Join(r.root, "messages.jsonl")
}

func (r *Room) profilesPath() string {
	return filepath.Join(r.root, "profiles.json")
}

func (r *Room) presenceDir() string {
	return filepath.Join(r.root, "presence")
}

func (r *Room) presencePath(sessionID string) string {
	return filepath.Join(r.presenceDir(), sessionID+".json")
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+newID()+".tmp")
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func newID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
