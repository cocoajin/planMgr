#!/bin/bash
set -euo pipefail
dest="$HOME/Library/Application Support/PlanMgrGo"
plist="$HOME/Library/LaunchAgents/cn.planmgr.go.plist"
domain="gui/$(id -u)"
daemon_plist="/Library/LaunchDaemons/cn.planmgr.go.$(id -u).plist"
label="cn.planmgr.go.$(id -u)"
[[ -d "$dest" ]] || { echo '未安装，无需清理'; exit 0; }
[[ ! -L "$dest" && ! -L "$plist" && -f "$dest/.planmgr-go-install" && "$(cat "$dest/.planmgr-go-install")" == planmgr-go-v1 ]] || { echo '不是本安装器的目录，拒绝删除'; exit 1; }
if [[ -e "$daemon_plist" ]]; then
 sudo -v
 if sudo launchctl print "system/$label" >/dev/null 2>&1; then sudo launchctl bootout "system/$label"; fi
 sudo rm -f "$daemon_plist"
fi
if [[ -f "$dest/data/control.json" ]]; then "$dest/planmgr" --data-dir "$dest/data" --stop; fi
if launchctl print "$domain/cn.planmgr.go" >/dev/null 2>&1; then launchctl bootout "$domain/cn.planmgr.go"; fi
rm -f "$plist"
rm -rf -- "$dest"
echo '程序、账号、任务和日志已清除。'
if [[ -t 0 ]]; then read -r -p '按回车关闭窗口…' || true; fi
