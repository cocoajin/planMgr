#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo '请使用 root 用户运行'; exit 1; }
src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
install -d -m 755 /opt/planmgr-go
install -d -m 700 /var/lib/planmgr-go
if [ -x /opt/planmgr-go/planmgr ]; then /opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go --service stop || true; /opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go --service uninstall || true; fi
install -m 755 "$src/planmgr" /opt/planmgr-go/planmgr
install -m 644 "$src/README.md" /opt/planmgr-go/README.md
/opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go --service install
/opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go --service start
systemctl enable PlanMgrGo
systemctl is-active --quiet PlanMgrGo
echo '安装完成；默认监听 127.0.0.1:8600。服务名 PlanMgrGo，数据 /var/lib/planmgr-go。'
