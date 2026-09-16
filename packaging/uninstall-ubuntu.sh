#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo '请用 sudo 运行'; exit 1; }
if [ -x /opt/planmgr-go/planmgr ]; then
 /opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go --service stop
 /opt/planmgr-go/planmgr --data-dir /var/lib/planmgr-go --service uninstall
 rm -rf /opt/planmgr-go
fi
if [ "${1:-}" = --purge ]; then rm -rf /var/lib/planmgr-go; echo '程序和全部任务数据已清除'; else echo '程序已卸载，数据保留在 /var/lib/planmgr-go（加 --purge 可清除）'; fi
