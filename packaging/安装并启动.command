#!/bin/bash
set -euo pipefail
src="$(cd "$(dirname "$0")" && pwd)"
dest="$HOME/Library/Application Support/PlanMgrGo"
legacy_plist="$HOME/Library/LaunchAgents/cn.planmgr.go.plist"
plist="/Library/LaunchDaemons/cn.planmgr.go.$(id -u).plist"
plist_tmp="$(mktemp -t planmgr-plist)"
domain="system"
label="cn.planmgr.go.$(id -u)"
finish(){ if [[ -t 0 ]]; then read -r -p '按回车关闭窗口…' || true; fi; }
trap 'echo "未完成，请查看上方错误。"; finish' ERR
[[ "$(uname -s)" == Darwin && "$(id -u)" != 0 ]] || { echo '请用当前 Mac 用户双击运行，不要使用 sudo。'; exit 1; }
[[ ! -L "$dest" && ! -L "$plist" ]] || { echo '安装目录异常'; exit 1; }
if [[ -e "$dest" && ! -f "$dest/.planmgr-go-install" ]]; then echo '目标目录已有其他数据，已停止。'; exit 1; fi
sudo -v
if [[ -f "$dest/data/control.json" ]]; then "$dest/planmgr" --data-dir "$dest/data" --stop; fi
if launchctl print "gui/$(id -u)/cn.planmgr.go" >/dev/null 2>&1; then launchctl bootout "gui/$(id -u)/cn.planmgr.go"; fi
rm -f "$legacy_plist"
if sudo launchctl print "$domain/$label" >/dev/null 2>&1; then sudo launchctl bootout "$domain/$label"; fi
mkdir -p "$dest/data" "$dest/logs" "$HOME/Library/LaunchAgents"
if [[ "$src" != "$dest" ]]; then cp "$src/planmgr" "$dest/planmgr.new"; chmod 700 "$dest/planmgr.new"; mv "$dest/planmgr.new" "$dest/planmgr"; cp "$src/安装并启动.command" "$src/卸载并清理数据.command" "$src/README.md" "$dest/"; fi
printf 'planmgr-go-v1' > "$dest/.planmgr-go-install"
# Generate a system launch daemon running as the installing user.
rm -f "$plist_tmp"
/usr/libexec/PlistBuddy -c "Add :Label string $label" -c "Add :UserName string $(id -un)" "$plist_tmp"
/usr/libexec/PlistBuddy -c 'Add :ProgramArguments array' -c "Add :ProgramArguments:0 string '$dest/planmgr'" -c 'Add :ProgramArguments:1 string --data-dir' -c "Add :ProgramArguments:2 string '$dest/data'" "$plist_tmp"
/usr/libexec/PlistBuddy -c 'Add :RunAtLoad bool true' -c 'Add :KeepAlive dict' -c 'Add :KeepAlive:SuccessfulExit bool false' -c 'Add :ThrottleInterval integer 10' -c "Add :StandardOutPath string '$dest/logs/launcher.log'" -c "Add :StandardErrorPath string '$dest/logs/launcher-error.log'" -c 'Add :EnvironmentVariables dict' -c 'Add :EnvironmentVariables:PATH string /opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin' "$plist_tmp"
sudo install -o root -g wheel -m 644 "$plist_tmp" "$plist"
rm -f "$plist_tmp"
sudo launchctl enable "$domain/$label"
sudo launchctl bootstrap "$domain" "$plist"
ready=0
for ((i=0;i<40;i++)); do if [[ -f "$dest/data/control.json" ]] && curl -fsS --max-time 1 'http://127.0.0.1:8600/api.php?action=session' >/dev/null 2>&1; then ready=1; break; fi; sleep .25; done
[[ "$ready" == 1 ]] || { echo "启动失败，请检查 $dest/logs/launcher-error.log（可能端口被占用）。"; exit 1; }
echo '安装完成：http://127.0.0.1:8600；已设置开机自动启动，无需安装运行环境。'
open 'http://127.0.0.1:8600'
finish
