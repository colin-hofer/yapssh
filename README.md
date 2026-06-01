# yapssh

`yapssh` is a single-room terminal chat app written in Go with Bubble Tea. It is designed to be dropped into over SSH: users connect, the TUI starts immediately, and everyone lands in the same shared room.

The main mode is `yapssh tui`, which works well with Tailscale SSH because Tailscale can keep owning port 22 and session authorization. There is also `yapssh serve` for a terminal.shop-style standalone SSH server on another port.

## Build

```bash
go build -buildvcs=false ./cmd/yapssh
```

## Run Locally

```bash
./yapssh --name Colin
```

Controls:

- `enter`: send
- `shift+enter`: insert a newline when supported by the terminal
- `ctrl+r`: rename yourself
- `/name <name>` or `/nick <name>`: rename yourself
- `/me <action>`: send an action line
- `pgup` / `pgdn`: scroll
- `ctrl+c`: quit

## Tailscale SSH Setup

Use a shared data directory and launch the TUI as the SSH entrypoint:

```bash
sudo install -m 755 ./yapssh /usr/local/bin/yapssh
sudo mkdir -p /var/lib/yapssh
sudo chown chat:chat /var/lib/yapssh
```

For a dedicated `chat` Unix user, set its login shell to a small wrapper:

```bash
#!/usr/bin/env sh
exec /usr/local/bin/yapssh tui --data /var/lib/yapssh --room tailnet
```

If your SSH setup supports a forced command, point it at the same command:

```text
ForceCommand /usr/local/bin/yapssh tui --data /var/lib/yapssh --room tailnet
```

`tui` mode derives a stable default user id from `YAPSSH_ID`, or from the SSH client address in `SSH_CONNECTION`, so multiple people can connect through the same Unix account and then rename themselves.

## Standalone SSH Server

For terminal.shop-style direct access on a tailnet-only port:

```bash
./yapssh serve --listen :23234 --data /var/lib/yapssh --room tailnet
ssh -p 23234 chat@<tailscale-hostname>
```

The standalone server creates a persistent host key at `<data>/ssh_host_rsa_key` unless `--host-key` is provided. Bind it to a tailnet-only interface or protect the port with firewall rules.

## Storage

State is file-backed so separate SSH-launched processes share one room:

- `messages.jsonl`: append-only room history
- `profiles.json`: persisted display names by user id
- `presence/*.json`: live session heartbeat files

The default data directory is `$XDG_STATE_HOME/yapssh` or `~/.local/state/yapssh`. Override it with `--data` or `YAPSSH_DATA`.
