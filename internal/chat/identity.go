package chat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Identity identifies a person or SSH endpoint in the room.
type Identity struct {
	UserID string
	Name   string
}

// InferLocalIdentity builds a stable-enough identity for OpenSSH/Tailscale SSH
// command mode. YAPSSH_ID and YAPSSH_NAME override the detected values.
func InferLocalIdentity(idOverride, nameOverride string) Identity {
	id := firstNonEmpty(idOverride, os.Getenv("YAPSSH_ID"))
	name := firstNonEmpty(nameOverride, os.Getenv("YAPSSH_NAME"), os.Getenv("USER"), os.Getenv("LOGNAME"), "guest")

	if id == "" {
		if remoteIP := remoteIPFromSSHConnection(os.Getenv("SSH_CONNECTION")); remoteIP != "" {
			id = fmt.Sprintf("%s@%s", firstNonEmpty(os.Getenv("USER"), "ssh"), remoteIP)
		}
	}
	if id == "" {
		host, _ := os.Hostname()
		id = fmt.Sprintf("%s@%s", firstNonEmpty(os.Getenv("USER"), "local"), firstNonEmpty(host, "localhost"))
	}
	if strings.TrimSpace(id) == "" {
		id = "guest-" + randomHex(4)
	}
	return Identity{
		UserID: strings.TrimSpace(id),
		Name:   NormalizeName(name),
	}
}

func remoteIPFromSSHConnection(value string) string {
	fields := strings.Fields(value)
	if len(fields) < 1 {
		return ""
	}
	return fields[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func randomHex(bytes int) string {
	if bytes < 1 {
		bytes = 1
	}
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "0000"
	}
	return hex.EncodeToString(buf)
}
