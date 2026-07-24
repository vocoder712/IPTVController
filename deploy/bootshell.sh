#!/bin/sh
set -eu

# OpenWrt calls this single entry inside the ufw container.
/opt/apps/cpolar/cpolar start-all -dashboard=on -daemon=on -config=/opt/apps/cpolar/cpolar.yml &

BASE=/opt/apps/iptv-control
LOG=/var/log/iptv-control.log
export IPTV_CONTROL_ADDR="192.168.1.1:8088"
export IPTV_CONTROL_INTERFACE="eth1"
export IPTV_CONTROL_IP="/sbin/ip"
export IPTV_CONTROL_DBUS_SEND="/usr/bin/dbus-send"
export IPTV_CONTROL_REAL=1
export IPTV_CONTROL_STATE_FILE="$BASE/state/state.log"

"$BASE/iptv-control" >>"$LOG" 2>&1 &
