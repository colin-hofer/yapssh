# yapssh

`yapssh` is a single-room terminal chat app written in Go with Bubble Tea. It is designed to be dropped into over SSH: users connect, the TUI starts immediately, and everyone lands in the same shared room.

Running `yapssh` starts the server. It does not open the chat locally. People who SSH into the running server see the chat room.

## Build

```bash
go build -buildvcs=false -o yapssh ./cmd/yapssh
```

## Run The Server

Terminal.shop-style access means `yapssh` must be the SSH server answering the address and port you connect to.

```bash
sudo ./yapssh --listen :22 --data /var/lib/yapssh --room tailnet
```

Then connect from another terminal or machine:

```bash
ssh chat@<tailscale-hostname>
```

`yapssh serve` is an explicit alias for the default server mode.

If you leave OpenSSH or Tailscale SSH answering port 22, `ssh <host>` will keep opening a normal shell. In that case either connect to the port where `yapssh` is actually listening, for example `ssh -p 23234 chat@<host>`, or move/disable the other SSH server for that address.

Controls:

- `enter`: send
- `shift+enter`: insert a newline when supported by the terminal
- `ctrl+r`: rename yourself
- `/name <name>` or `/nick <name>`: rename yourself
- `/me <action>`: send an action line
- `pgup` / `pgdn`: scroll
- `ctrl+c`: quit

## Tailscale SSH Setup

Install and run `yapssh` as a long-lived process on the VM. For bare `ssh chat@<tailscale-hostname>` to enter the chat, `yapssh` must own port 22 on the Tailscale address:

```bash
sudo install -m 755 ./yapssh /usr/local/bin/yapssh
sudo mkdir -p /var/lib/yapssh
sudo chown -R yapssh:yapssh /var/lib/yapssh
sudo /usr/local/bin/yapssh --listen :22 --data /var/lib/yapssh --room tailnet
```

Tailscale SSH cannot be configured to use a different port and claims port 22 on the Tailscale IP when enabled. If Tailscale SSH is enabled for this VM, it will answer `ssh <tailscale-hostname>` before `yapssh` can. Disable Tailscale SSH for this host or use a different `yapssh` port and connect with `ssh -p`:

```bash
sudo tailscale set --ssh=false
```

The server creates a persistent host key at `<data>/ssh_host_rsa_key` unless `--host-key` is provided. Bind it to a tailnet-only interface or protect the port with Tailscale ACLs/firewall rules. Running on port 22 usually requires root or `cap_net_bind_service`.

## Tailscale SSH Forced Command Alternative

If you specifically want Tailscale SSH to keep managing port 22 and authentication, do not run the `yapssh` SSH server on port 22. Instead, create a dedicated Unix user named `chat` whose login shell runs the TUI. Your normal admin SSH user keeps a normal shell.

Install the binary and login-shell wrapper:

```bash
scripts/install-chat-user.sh
```

The script builds `./yapssh`, installs `/usr/local/bin/yapssh`, installs `/usr/local/bin/yapssh-chat-shell`, registers the shell, creates or updates the `chat` user, and gives that user ownership of `/var/lib/yapssh`.

Manual equivalent:

```bash
go build -buildvcs=false -o ./yapssh ./cmd/yapssh
sudo install -m 755 ./yapssh /usr/local/bin/yapssh
sudo install -m 755 scripts/yapssh-chat-shell /usr/local/bin/yapssh-chat-shell
grep -qxF /usr/local/bin/yapssh-chat-shell /etc/shells || echo /usr/local/bin/yapssh-chat-shell | sudo tee -a /etc/shells

sudo mkdir -p /var/lib/yapssh
if id chat >/dev/null 2>&1; then
  sudo usermod --shell /usr/local/bin/yapssh-chat-shell chat
else
  sudo useradd --create-home --shell /usr/local/bin/yapssh-chat-shell chat
fi
sudo chown -R chat:chat /var/lib/yapssh
```

Make sure your Tailscale SSH policy allows people to connect to this VM as the SSH user `chat`, then test from another terminal:

```bash
ssh -t chat@<tailscale-hostname>
```

That drops only the `chat` user into the room. Your current admin user remains available for VM maintenance.

If you are using OpenSSH instead of Tailscale SSH, an `sshd_config` `ForceCommand` for the `chat` user also works:

```text
Match User chat
  ForceCommand /usr/local/bin/yapssh shell --data /var/lib/yapssh --room tailnet
  PermitTTY yes
```

That mode still drops users into the chat immediately, but the SSH server is Tailscale/OpenSSH, not `yapssh`.

## Local Debug Client

For development only, `yapssh tui --data <dir> --room <name>` opens a local chat client against the same file-backed room. Normal users should connect over SSH to the server.

## Storage

State is file-backed so sessions survive server restarts:

- `messages.jsonl`: append-only room history
- `profiles.json`: persisted display names by user id
- `presence/*.json`: live session heartbeat files

The default data directory is `$XDG_STATE_HOME/yapssh` or `~/.local/state/yapssh`. Override it with `--data` or `YAPSSH_DATA`.
