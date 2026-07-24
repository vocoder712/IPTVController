# IPTVController

面向 SG631Z 光猫 LAN2 IPTV 端口的轻量控制服务。第一阶段提供状态查询、
HTTP API 和 H5 手动开关，不包含定时计划或认证。

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
- `/sys/class/net/eth1` 和 `/bin/ip` 可用；
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
