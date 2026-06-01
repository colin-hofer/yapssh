#!/usr/bin/env sh
set -eu

data_dir="${YAPSSH_DATA:-/var/lib/yapssh}"
room="${YAPSSH_ROOM:-tailnet}"
chat_user="${YAPSSH_CHAT_USER:-chat}"

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
binary="$repo_root/yapssh"

cd "$repo_root"
go build -buildvcs=false -o "$binary" ./cmd/yapssh

sudo install -m 755 "$binary" /usr/local/bin/yapssh
sudo install -m 755 "$repo_root/scripts/yapssh-chat-shell" /usr/local/bin/yapssh-chat-shell

if ! grep -qxF /usr/local/bin/yapssh-chat-shell /etc/shells; then
	echo /usr/local/bin/yapssh-chat-shell | sudo tee -a /etc/shells >/dev/null
fi

sudo mkdir -p "$data_dir"
if id "$chat_user" >/dev/null 2>&1; then
	sudo usermod --shell /usr/local/bin/yapssh-chat-shell "$chat_user"
else
	sudo useradd --create-home --shell /usr/local/bin/yapssh-chat-shell "$chat_user"
fi
sudo chown -R "$chat_user:$chat_user" "$data_dir"

echo "installed /usr/local/bin/yapssh"
echo "configured $chat_user with /usr/local/bin/yapssh-chat-shell"
echo "room data: $data_dir"
echo "default room: $room"
