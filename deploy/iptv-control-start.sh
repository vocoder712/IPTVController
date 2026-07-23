#!/bin/sh
set -eu
BASE=/opt/cu/apps/apps/opt/apps/iptv-control
LOG=/var/log/iptv-control.log
export IPTV_CONTROL_ADDR="192.168.1.1:8088"
export IPTV_CONTROL_INTERFACE="eth1"
# 开发/验证阶段默认模拟；设备上确认无损能力后再显式设置 IPTV_CONTROL_REAL=1。
# export IPTV_CONTROL_REAL=1
exec "$BASE/iptv-control" >>"$LOG" 2>&1
