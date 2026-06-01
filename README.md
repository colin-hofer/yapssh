# yapssh

`yapssh` is a single-room terminal chat app written in Go with Bubble Tea. It is designed to be dropped into over SSH: users connect, the TUI starts immediately, and everyone lands in the same shared room.

Running `yapssh` starts the server. It does not open the chat locally. People who SSH into the running server see the chat room.

## Build

```bash
go build -buildvcs=false ./cmd/yapssh
```

## Run The Server

```bash
./yapssh --listen :23234 --data /var/lib/yapssh --room tailnet
```

Then connect from another terminal or machine:

```bash
ssh -p 23234 chat@<tailscale-hostname>
```

`yapssh serve` is an explicit alias for the default server mode.

Controls:

- `enter`: send
- `shift+enter`: insert a newline when supported by the terminal
- `ctrl+r`: rename yourself
- `/name <name>` or `/nick <name>`: rename yourself
- `/me <action>`: send an action line
- `pgup` / `pgdn`: scroll
- `ctrl+c`: quit

## Tailscale SSH Setup

Install and run `yapssh` as a long-lived process on the VM:

```bash
sudo install -m 755 ./yapssh /usr/local/bin/yapssh
sudo mkdir -p /var/lib/yapssh
sudo chown -R yapssh:yapssh /var/lib/yapssh
/usr/local/bin/yapssh --listen :23234 --data /var/lib/yapssh --room tailnet
```

The server creates a persistent host key at `<data>/ssh_host_rsa_key` unless `--host-key` is provided. Bind it to a tailnet-only interface or protect the port with Tailscale ACLs/firewall rules.

## Local Debug Client

For development only, `yapssh tui --data <dir> --room <name>` opens a local chat client against the same file-backed room. Normal users should connect over SSH to the server.

## Storage

State is file-backed so sessions survive server restarts:

- `messages.jsonl`: append-only room history
- `profiles.json`: persisted display names by user id
- `presence/*.json`: live session heartbeat files

The default data directory is `$XDG_STATE_HOME/yapssh` or `~/.local/state/yapssh`. Override it with `--data` or `YAPSSH_DATA`.
