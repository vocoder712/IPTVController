# IPTVController

> ### 你是否在为家里的儿童看电视时间过长而烦恼？
>
> ### 你是否在为家里的儿童看电视时间过长而烦恼？
> 
> ### 你是否在为家里的儿童看电视时间过长而烦恼？
>
> #### 如果你使用的是 SG631Z 光猫，本文档介绍的 IPTVController 可以帮助你轻松控制 LAN2 IPTV 端口的开关和观看时间。帮助你管理儿童的电视观看时间，避免过度沉迷。

## 开箱即用教程

### 前提条件

- SG631Z 光猫
- 已经拥有 telnet 超级用户登录权限

### 部署

- 下载 IPTVController 的最新版本，解压到光猫的 LXC 容器 APP 目录 `/opt/cu/apps/apps/opt/apps`，新建目录 `iptv-control`，将解压后的文件放入该目录。
- 下载 bootshell.sh 脚本，替换 `/opt/cu/apps/apps/root/scripts/bootshell.sh` 文件。确保上传之后的脚本具有可执行权限。
- 重启光猫。
- 访问 <http://192.168.1.1:8088/>，即可看到 IPTVController 的 H5 页面。

## 三种运行方式

| 方式 | 运行位置 | `IPTV_CONTROL_REAL` | 是否操作 LAN2 | 用途 |
| --- | --- | --- | --- | --- |
| 本地开发模式 | Windows/Linux 开发机 | 不设置 | 否 | 开发 H5 和 API |
| 设备模拟模式 | SG631Z/OpenWrt | 不设置 | 否 | 验证 ARM 程序、监听地址和启动脚本 |
| 设备实际模式 | SG631Z/OpenWrt | 设置为 `1` | 是 | 实际查询和开关 `eth1` |

关键规则：只有进程启动时检测到 `IPTV_CONTROL_REAL=1` 才会读取 Linux
接口状态并执行 `ip link`。未设置或设置为其他值时都是模拟模式，页面按钮
只改变进程内存中的模拟状态，不会影响电视。

## 本地开发

需要 Go 1.22 或更新版本。PowerShell 示例：

```powershell
$env:GOCACHE = "$PWD/.cache/go-build"
$env:GOMODCACHE = "$PWD/.cache/go-mod"
$env:GOPATH = "$PWD/.cache/gopath"

Remove-Item Env:IPTV_CONTROL_REAL -ErrorAction SilentlyContinue
$env:IPTV_CONTROL_ADDR = "127.0.0.1:8088"
go test ./...
go run ./cmd/iptv-control
```

然后访问 <http://127.0.0.1:8088/>。此时：

- 页面和 API 可以正常操作；
- `oper_state` 和 `carrier` 显示 `simulated`；
- 不需要 root，不读取 `/sys/class/net`，也不会执行 `/bin/ip`；
- 模拟状态只保存在内存中，服务重启后丢失。

## 构建 SG631Z ARMv7 程序

```powershell
$env:GOOS = "linux"
$env:GOARCH = "arm"
$env:GOARM = "5"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags="-s -w" -o dist/iptv-control ./cmd/iptv-control
```

SG631Z 虽然报告 ARMv7，但实测 CPU Features 不包含 VFP。必须使用
`GOARM=5` 的软浮点兼容构建；`GOARM=7` 二进制会在启动时报告
`Illegal instruction`。

生成文件为 `dist/iptv-control`。先以设备模拟模式验证程序和 HTTP 服务，
确认无误后再显式启用实际模式。

## 设备实际模式

实际模式必须满足：

- 程序在 SG631Z/OpenWrt 环境中运行；
- 运行用户为 root；
- `/sys/class/net/eth1`、`/sbin/ip`、`/usr/bin/dbus-send` 和 system bus
  socket 可用；
- 管理连接走 LAN3 或其他链路，不能依赖即将关闭的 LAN2；
- 服务仅暴露在可信管理网，因为当前版本没有认证。

启用方式是启动进程前设置：

```sh
export IPTV_CONTROL_REAL=1
```

实际模式下，关闭按钮会执行：

```sh
/sbin/ip link set dev eth1 down
```

开启按钮会执行：

```sh
/sbin/ip link set dev eth1 up
```

完整的设备模拟部署、实际部署、API 调用、安全切换和故障排查步骤见
[`docs/运行与部署.md`](docs/运行与部署.md)。设备环境和已验证的 LAN2
直接控制方法见 [`docs/基本信息.md`](docs/基本信息.md)，阶段规划见
[`.codex/PLAN.md`](.codex/PLAN.md)。

## 智能限时

智能限时的程序默认配置为关闭；设备持久状态可覆盖该默认值。核心状态机、
H5 配置和持久化已完成开发机与设备基础验证。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `IPTV_LIMITER_ENABLED` | 未设置 | 只有精确值 `1` 才启用 |
| `IPTV_LIMITER_POLL_INTERVAL` | `30s` | 载波查询间隔 |
| `IPTV_LIMITER_WATCH_LIMIT` | `20m` | 连续观看限时 |
| `IPTV_LIMITER_CYCLE` | `30s` | 干预周期 |
| `IPTV_LIMITER_MIN_DOWN_DURATION` | `1s` | 随机阻断下限；必须为 1–26 整数秒 |
| `IPTV_LIMITER_MAX_DOWN_DURATION` | `26s` | 随机阻断上限；必须为 1–26 整数秒且不小于下限 |
| `IPTV_CONTROL_STATE_FILE` | 未设置 | 持久状态日志；设备使用 `$BASE/state/state.log` |

达到限时后按 30 秒周期循环，每周期先在家长设置的 1–26 秒范围内随机阻断，
再接通剩余时间。连续两个接通窗口结束时都没有载波，才认为电视已关闭。
用户通过 API 手动开关时，
手动动作优先并清除当前自动计时。完整状态转移表见
[`.codex/PLAN.md`](.codex/PLAN.md)。

进入观看后，如果机顶盒关机或网线被拔出，服务会保留累计观看时间并关闭
LAN2 30 分钟；干预阶段则采用连续两个接通窗口无载波的判定。冷却到期才恢复
端口并清空累计时间。冷却截止时间持久化，重启不能绕过。H5 的“立即进入干预”
按钮仅在 `watching` 状态可用。

SG631Z 实际模式下，观看状态来自厂商 DBus 的只读 `LAN2Status`，不使用启动
初态可能错误的 `/sys/class/net/eth1/carrier`。DBus 查询失败会作为状态读取
错误处理，不会回退并误计观看时间。

H5 页面可直接启用或停用智能限时，并设置 1 到 1440 分钟的单次最长观看时间。
设置通过以下接口立即生效：

```http
POST /api/v1/limiter
Content-Type: application/json

{"enabled":true,"max_watch_minutes":20,"block_min_seconds":1,"block_max_seconds":26}
```

四个字段均为必填；旧 `block_seconds` 请求不再兼容。智能限时保持启用时，
修改 `max_watch_minutes`、`block_min_seconds` 或 `block_max_seconds` 不会清空
当前计时，也不会退出干预或冷却状态。

观看期间可由家长立即进入干预：

```http
POST /api/v1/intervene
```

设置路径非空时，服务使用带 CRC 的追加日志持久化配置和累计观看时间。进度
每 5 分钟检查点一次，配置写入在 30 秒窗口内合并，日志达到 64 KiB 时原子
压缩。正常退出会强制刷新；异常断电最多回退约 5 分钟。重启后绝不直接恢复
为周期阻断阶段；未结束的 30 分钟 cooldown 除外，它会继续保持 LAN2 关闭。
