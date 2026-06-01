package sshserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/colin-hofer/yapssh/internal/chat"
	"github.com/colin-hofer/yapssh/internal/tui"
)

type Config struct {
	Listen      string
	DataDir     string
	Room        string
	HostKeyPath string
}

func Run(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = "127.0.0.1:23234"
	}
	if strings.TrimSpace(cfg.Room) == "" {
		cfg.Room = chat.DefaultRoomName
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		cfg.DataDir = chat.DefaultRoot()
	}
	if strings.TrimSpace(cfg.HostKeyPath) == "" {
		cfg.HostKeyPath = filepath.Join(cfg.DataDir, "ssh_host_rsa_key")
	}
	if err := ensureHostKey(cfg.HostKeyPath); err != nil {
		return err
	}

	room, err := chat.Open(cfg.DataDir, cfg.Room)
	if err != nil {
		return err
	}

	server := &gliderssh.Server{
		Addr:        cfg.Listen,
		Handler:     handler(room),
		IdleTimeout: 12 * time.Hour,
	}
	if err := server.SetOption(gliderssh.HostKeyFile(cfg.HostKeyPath)); err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err = server.ListenAndServe()
	if errors.Is(err, gliderssh.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func handler(room *chat.Room) gliderssh.Handler {
	return func(sess gliderssh.Session) {
		pty, winCh, ok := sess.Pty()
		if !ok {
			_, _ = io.WriteString(sess, "yapssh requires a PTY. Try: ssh -t <host>\n")
			_ = sess.Exit(1)
			return
		}

		client, err := room.Join(identityFromSession(sess))
		if err != nil {
			_, _ = fmt.Fprintf(sess, "failed to join chat: %v\n", err)
			_ = sess.Exit(1)
			return
		}

		windowChanges := make(chan tui.WindowSize, 8)
		go func() {
			defer close(windowChanges)
			for win := range winCh {
				windowChanges <- tui.WindowSize{Width: win.Width, Height: win.Height}
			}
		}()

		err = tui.Run(sess.Context(), client, tui.Options{
			Input:         sess,
			Output:        sess,
			Width:         pty.Window.Width,
			Height:        pty.Window.Height,
			WindowChanges: windowChanges,
		})
		if err != nil {
			_, _ = fmt.Fprintf(sess, "yapssh exited with error: %v\n", err)
			_ = sess.Exit(1)
			return
		}
		_ = sess.Exit(0)
	}
}

func identityFromSession(sess gliderssh.Session) chat.Identity {
	user := strings.TrimSpace(sess.User())
	if user == "" {
		user = "guest"
	}
	name := user
	id := ""
	if key := sess.PublicKey(); key != nil {
		id = ssh.FingerprintSHA256(key)
	} else if remote := sess.RemoteAddr(); remote != nil {
		host, _, err := net.SplitHostPort(remote.String())
		if err != nil || host == "" {
			host = remote.String()
		}
		id = user + "@" + host
	}
	if id == "" {
		id = user
	}
	return chat.Identity{UserID: id, Name: chat.NormalizeName(name)}
}

func ensureHostKey(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}
