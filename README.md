# IPTVController

面向 SG631Z 光猫 LAN2 IPTV 端口的轻量控制服务。第一阶段只实现接口能力检测、状态展示和手动开关，不包含定时计划。

## 本地开发

需要 Go 1.22 或更新版本：

```powershell
go test ./...
$env:IPTV_CONTROL_INTERFACE = "lo"
go run ./cmd/iptv-control
```

开发模式不会执行真实 `ip link`。设备部署验证完成后，必须显式设置 `IPTV_CONTROL_REAL=1` 才启用真实控制。

## ARMv7 构建

```powershell
$env:GOOS = "linux"
$env:GOARCH = "arm"
$env:GOARM = "7"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags="-s -w" -o dist/iptv-control ./cmd/iptv-control
```

详细阶段规划见 [`.codex/plan.md`](.codex/plan.md)，设备信息见 [`docs/基本信息.md`](docs/基本信息.md)。
