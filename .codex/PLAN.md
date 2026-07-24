# 第一阶段：LAN2 开关框架与容器能力验证

## 目标

第一阶段只验证容器内服务控制主系统 LAN2（`eth1`）的能力，并提供一个最小 H5 开关页面。暂不实现计划任务、HTTPS、Android、远程访问、认证、IPTV VLAN/组播分析或厂商网页 API。

## 已知环境

- SG631Z，ARMv7 Linux 4.19；根文件系统只读。
- OpenWrt 运行在 `ufw` LXC 中，`lxc.net.0.type = none`，与主系统共享 network/user namespace。
- 容器可见 `eth0`-`eth3`，LAN2 为 `eth1`，并具备 `CAP_NET_ADMIN`。
- 持久应用目录：`/opt/cu/apps/apps/opt/apps/`。
- 容器启动脚本目录：`/opt/cu/apps/apps/root/scripts/`。
- 服务首版监听 `192.168.1.1:8088`，不启用 HTTPS。

## 首版实现

- Go 标准库单文件服务，内嵌 H5 页面。
- `GET /api/v1/status`、`POST /api/v1/port`、`GET /healthz`。
- `PortController` 抽象；开发默认模拟执行器，设备上设置 `IPTV_CONTROL_REAL=1` 后调用 `/bin/ip`。
- 启动时检查 UID、`eth1`、能力和工具；定期读取接口状态并报告厂商回拉。
- 状态和最后一次手动状态使用原子 JSON 保存；日志优先写入易失目录。
- 提供 `deploy/bootshell.sh` 固定启动入口模板，使用容器路径
  `/opt/apps/iptv-control`；真实控制默认禁用。

## 验证分层

1. 只读检查 namespace、能力、`eth1` 和 `/bin/ip`。
2. 无损 `ip link set eth1 up` 检查；若原状态为 down 不改变它。
3. 受控 down/up 测试仅由显式命令触发，当前不自动执行。
4. 观察 1-5 分钟，记录厂商进程是否回拉接口。

## 后续预留

保留控制器接口，后续增加周计划、临时覆盖、状态对账以及厂商 API 适配器。
