package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/colin-hofer/yapssh/internal/chat"
	"github.com/colin-hofer/yapssh/internal/sshserver"
	"github.com/colin-hofer/yapssh/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runServer(nil)
	}
	switch args[0] {
	case "tui":
		return runTUI(args[1:])
	case "serve":
		return runServer(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	case "version", "-v", "--version":
		fmt.Println("yapssh dev")
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return runServer(args)
		}
		return fmt.Errorf("unknown command %q\n\nrun `yapssh help` for usage", args[0])
	}
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("yapssh tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data", envOr("YAPSSH_DATA", chat.DefaultRoot()), "state directory")
	roomName := fs.String("room", envOr("YAPSSH_ROOM", chat.DefaultRoomName), "room name")
	name := fs.String("name", "", "display name override")
	userID := fs.String("id", "", "stable user id override")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	room, err := chat.Open(*dataDir, *roomName)
	if err != nil {
		return err
	}
	client, err := room.Join(chat.InferLocalIdentity(*userID, *name))
	if err != nil {
		return err
	}
	return tui.Run(ctx, client, tui.Options{})
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("yapssh", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", envOr("YAPSSH_LISTEN", ":22"), "SSH listen address")
	dataDir := fs.String("data", envOr("YAPSSH_DATA", chat.DefaultRoot()), "state directory")
	roomName := fs.String("room", envOr("YAPSSH_ROOM", chat.DefaultRoomName), "room name")
	hostKey := fs.String("host-key", "", "SSH host key path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := sshserver.Config{
		Listen:      *listen,
		DataDir:     *dataDir,
		Room:        *roomName,
		HostKeyPath: *hostKey,
	}
	fmt.Fprintf(os.Stderr, "listening on %s, room %q, data %s\n", cfg.Listen, chat.NormalizeRoomName(cfg.Room), cfg.DataDir)
	err := sshserver.Run(ctx, cfg)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func printUsage() {
	fmt.Println(`usage:
  yapssh [--listen ADDR] [--data DIR] [--room NAME] [--host-key PATH]
  yapssh serve [--listen ADDR] [--data DIR] [--room NAME] [--host-key PATH]
  yapssh tui [--data DIR] [--room NAME] [--name NAME] [--id ID]

modes:
  default run the SSH chat server; people who SSH into it see the room
  serve   explicit alias for the default server mode
  tui     local development client; not used for normal server operation

environment:
  YAPSSH_DATA    state directory
  YAPSSH_ROOM    room name
  YAPSSH_LISTEN  server listen address
  YAPSSH_ID      stable user id for local tui mode
  YAPSSH_NAME    default display name for local tui mode`)
}
