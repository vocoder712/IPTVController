#!/bin/sh
set -eu
BASE=/opt/cu/apps/apps/opt/apps/iptv-control
LOG=/var/log/iptv-control.log
export IPTV_CONTROL_ADDR="192.168.1.1:8088"
export IPTV_CONTROL_INTERFACE="eth1"

# 默认是设备模拟模式：
# - 不读取或修改 eth1；
# - 页面/API 只维护进程内存中的模拟状态。
#
# 只有完成模拟模式 HTTP 验证、确认管理连接不走 LAN2，并准备好恢复手段后，
# 才取消下一行注释以启用实际模式。实际模式会真实执行 ip link。
# export IPTV_CONTROL_REAL=1

exec "$BASE/iptv-control" >>"$LOG" 2>&1
