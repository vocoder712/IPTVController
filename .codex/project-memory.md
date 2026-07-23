# IPTVController 项目记忆

最后更新：2026-07-23（Asia/Shanghai）

## 使用规则

- 每个任务开始前先阅读 `.codex/PLAN.md` 和本文件。
- 每个任务开始时在“当前任务”中记录目标、起始状态和限制。
- 每个任务完成后更新“当前进度”“验证记录”“已知问题”和“下一步”，不要把计划中的功能误写成已经实现。
- 工作区可能包含用户尚未提交的改动；修改前先运行 `git status --short` 和相关文件的 `git diff`，保留无关改动。
- 不在本文件复制光猫密码等敏感信息；需要时查看 `docs/基本信息.md`。

## 项目目标与阶段边界

项目为 SG631Z 光猫的 LAN2 IPTV 端口提供轻量控制服务。当前处于第一阶段：

- 验证 OpenWrt/LXC 环境控制主系统 LAN2 的能力；
- 提供最小 HTTP API 和 H5 手动开关页面；
- 默认禁止真实控制，只有显式设置 `IPTV_CONTROL_REAL=1` 才调用 `ip link`。

第一阶段暂不包含计划任务、HTTPS、Android 客户端、远程访问、认证、
IPTV VLAN/组播分析或厂商网页 API。阶段计划以 `.codex/PLAN.md` 为准。

## 设备与运行环境

- 设备：SG631Z，ARMv7，Linux 4.19。
- `/proc/cpuinfo` 的 Features 不含 `vfp`/`vfpv3`；Go 交叉构建必须使用
  `GOARM=5`。`GOARM=7` 能编译，但在设备启动时报 `Illegal instruction`。
- LAN2 对应 Linux 接口 `eth1`，已通过实机测试确认。
- OpenWrt 位于 `ufw` LXC，与主系统共享 network/user namespace，容器可见
  `eth0` 至 `eth3`，并具备控制网络接口所需能力。
- 光猫管理地址为 `192.168.1.1`；控制服务计划监听
  `192.168.1.1:8088`。
- 持久应用目录：`/opt/cu/apps/apps/opt/apps/`。
- 启动脚本目录：`/opt/cu/apps/apps/root/scripts/`。
- 光猫根文件系统和部分运行目录易失，设备每天可能自动重启；Telnet 重启后
  会关闭。
- 设备可用空间很少，应使用精简日志并优先写入 `/var` 等易失目录。
- 访问凭据只保存在 `docs/基本信息.md`，不要复制到代码、日志或本记忆文件。

## 项目结构

```text
.codex/
  PLAN.md                       第一阶段计划
  project-memory.md             本项目记忆
cmd/iptv-control/
  main.go                       Go 服务、控制器、HTTP API
  main_test.go                  HTTP handler 单元测试
  web/index.html                内嵌的最小 H5 页面
deploy/
  iptv-control-start.sh         光猫启动脚本模板
docs/
  基本信息.md                   拓扑、设备环境、可靠开关方法和实机记录
  运行与部署.md                 三种模式、部署步骤、API 验证和安全切换
AGENTS.md                       项目内 Codex 工作约束
Makefile                        测试、本地运行和 ARMv7 构建
README.md                       开发和构建入口
go.mod                         Go 1.22，无第三方依赖
```

## 当前已实现

### Go 服务

- 单个 Go `main` 包，使用标准库，无第三方依赖。
- 使用 `embed` 嵌入 `web/index.html`。
- `PortController` 抽象及 `ipController` 实现。
- API：
  - `GET /api/v1/status`
  - `POST /api/v1/port`，请求体为 `{"enabled": true|false}`
  - `GET /healthz`
- 真实模式通过 `/bin/ip link set dev <iface> up|down` 控制接口。
- 非真实模式不会读取 `/sys` 或执行 `ip link`，开关状态会稳定保存在进程
  内存中，便于 Windows 等本地环境开发。
- 真实模式要求进程 EUID 为 root。
- 真实状态从 sysfs 的 `flags`、`operstate` 和 `carrier` 读取；管理状态通过
  `flags & IFF_UP` 判断，避免把仅有 `BROADCAST/MULTICAST` 的 DOWN 接口
  误报为 UP。
- `LastChange` 会跨状态读取保留；sysfs 读取错误和 `ip link` 命令错误会写入
  `LastError`。
- 支持环境变量：
  - `IPTV_CONTROL_ADDR`，默认 `:8088`
  - `IPTV_CONTROL_INTERFACE`，默认 `eth1`
  - `IPTV_CONTROL_IP`，默认 `/bin/ip`
  - `IPTV_CONTROL_REAL=1` 启用真实控制
- 支持 SIGINT/SIGTERM 后关闭 HTTP 服务。

### H5 页面

- 提供开启/关闭 LAN2 两个按钮。
- 显示 enabled、管理状态、operstate 和 carrier。
- 每 5 秒刷新一次状态。
- 当前没有认证、操作确认、错误详情或自动恢复倒计时。

### 构建和部署材料

- `Makefile` 提供 `test`、`run`、`build-arm`。
- Go 缓存可放在项目 `.cache` 下，`.cache` 和 `dist` 已被忽略。
- `deploy/iptv-control-start.sh` 配置监听地址和 `eth1`，但
  `IPTV_CONTROL_REAL=1` 仍被注释，因此模板默认是模拟模式。
- `README.md` 提供本地开发、设备模拟、设备实际三种模式的快速对照。
- `docs/运行与部署.md` 提供从本地开发到 ARMv7 构建、设备模拟验证、实际
  模式切换、API 调用和故障排查的完整步骤。

## 尚未实现或尚未完成

以下内容写在阶段计划中，但当前代码尚未实现或尚未完成验证：

- 状态和最后一次手动状态的原子 JSON 持久化；
- 定期读取、状态对账和厂商进程回拉检测；
- 完整启动能力检查（namespace、CAP_NET_ADMIN、`/bin/ip` 可执行性等）；
- 设备重启后服务自动运行验证；
- 设备端 HTTP API/H5 的端到端真实开关验证；
- 观察 1 至 5 分钟以确认厂商进程是否会自动回拉接口。

## 已知实现问题和风险

1. 状态和最后一次操作只保存在进程内存中，服务重启后会丢失；原子 JSON
   持久化尚未实现。
2. 尚未定期对比“最后一次手动期望状态”和实际接口状态，因此无法自动识别
   或报告厂商进程回拉。
3. 启动检查仍不完整，目前只检查 root 和初始接口状态，尚未明确检查
   namespace、CAP_NET_ADMIN 和 `/bin/ip` 可执行性。
4. 真实 `ip link` 命令执行仍缺少注入式单元测试；当前已覆盖 IFF_UP 解析、
   sysfs 状态读取、模拟状态保留、HTTP 错误和 health 错误路径。
5. HTTP 服务目前无认证。第一阶段虽明确不做认证，但部署时只能暴露在可信
   管理网络，不能直接暴露到公网。

## 实机验证记录

### LAN2 直接控制

2026-07-23 已通过 LAN3 管理链路登录设备，并以 root 执行
`ip link set dev eth1 down|up`：

- 测试前：`eth1` 为 `UP,LOWER_UP`，`operstate=up`，`carrier=1`。
- 一次可审计测试的设备时间戳为：
  - DOWN：`1784807042`
  - UP：`1784807047`
- 两个时间戳相差 5 秒。
- 恢复两秒后：`operstate=up`、`carrier=1`。
- 恢复后的 5 秒内：RX 增加 4,646 bytes，TX 增加 5,401 bytes。
- 用户确认电视端出现预期的短暂断连。
- 可靠开关命令、状态判定和 `nohup` 延迟恢复兜底已写入
  `docs/基本信息.md`。

### HTTP 服务

2026-07-23 早期检查时，`192.168.1.1:8088` 可建立 TCP 连接但 HTTP
无响应。部署当前 GOARM=5 构建后该问题已消失：

- 模拟服务 PID 为 `15948`，监听 `192.168.1.1:8088`。
- 日志明确显示 `real=false`，进程环境中没有 `IPTV_CONTROL_REAL`。
- `/healthz`、状态 API、模拟开关 API 和 H5 首页均验证通过。
- 服务已安装到持久目录，但尚未通过设备重启验证开机自动运行。

## 本地验证基线

2026-07-23 在 Windows/PowerShell 工作区验证：

- `go test ./...`：通过。
- `go test -race ./...`：通过。
- `go vet ./...`：通过。
- `GOARM=7` 构建能完成，但设备运行时报 `Illegal instruction`，不可使用。
- `GOOS=linux GOARCH=arm GOARM=5 CGO_ENABLED=0 go build ...`：通过，并已
  在设备上成功运行。
- 生成的 `dist/iptv-control` 大小约 6,160,546 bytes。
- Go 模块：`iptvcontrol`，Go 版本 1.22。
- 本地开发模式已在 `127.0.0.1:8088` 跑通：
  - `/healthz` 返回 `ok=true`、`real_control=false`；
  - `/api/v1/status` 返回 `capability_check=simulation`；
  - POST 开启和关闭均只更新模拟状态；
  - `/` 返回 HTTP 200，包含 H5 标题、开启/关闭按钮及 API 调用脚本。
- 本次尝试使用页面浏览器连接器进行可视化验证时，连接器因执行环境缺少
  `sandboxPolicy` 元数据而无法建立会话；应用本身的 HTTP、HTML 和 API
  验证均通过。

推荐命令：

```powershell
$env:GOCACHE = "$PWD/.cache/go-build"
$env:GOMODCACHE = "$PWD/.cache/go-mod"
$env:GOPATH = "$PWD/.cache/gopath"
go test ./...

$env:GOOS = "linux"
$env:GOARCH = "arm"
$env:GOARM = "5"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags="-s -w" -o dist/iptv-control ./cmd/iptv-control
```

## Git 与工作区状态

- 最近提交：
  - `fb99543 修正了gitignore和许可证`
  - `95d9a48 初始提交`
- 2026-07-23 创建本记忆时，工作区已有未提交修改：
  `.gitignore`、`AGENTS.md`、`Makefile`、`README.md`、
  `cmd/iptv-control/main.go`、`cmd/iptv-control/main_test.go`、
  `docs/基本信息.md`。
- 其中 `docs/基本信息.md` 的 LAN2 可靠开关章节由本轮会话补充；其他修改
  在创建项目记忆前已存在，应视为用户工作并谨慎保留。

## 建议下一步

优先顺序建议如下：

1. 完善启动能力检查，包括 `/bin/ip`、接口、root 和能力信息。
2. 在合适时机重启设备，验证持久启动脚本能自动恢复模拟服务。
3. 在明确的恢复兜底下启用 `IPTV_CONTROL_REAL=1`，完成 API 端到端开关测试。
4. 实现原子状态持久化、周期对账及厂商回拉检测。

## 当前任务

任务：将当前 ARMv7 构建部署到 SG631Z 持久目录，并以设备模拟模式验证
HTTP API 和 H5。

状态：已完成。

完成结果：

- 本地 `go test ./...` 通过。
- 初始 GOARM=7 二进制上传和 SHA-256 校验成功，但设备因 CPU 无 VFP 支持
  报 `Illegal instruction`；服务未启动，未触发任何端口控制。
- 改用 `GOARM=5` 重新构建、上传和校验。正式二进制：
  - 路径：`/opt/cu/apps/apps/opt/apps/iptv-control/iptv-control`
  - 大小：6,160,546 bytes
  - SHA-256：`202ff3c4fdcf062ffacd468a0deb94e3cbc56f15ecc773cf8867a2fa1a64b75b`
- 启动脚本已安装到
  `/opt/cu/apps/apps/root/scripts/iptv-control-start.sh`，SHA-256 为
  `da209ee5ed781e102e1f9e6dbd41dc5d3002fd68f74c995f3727f24c9b2e4a74`，
  `IPTV_CONTROL_REAL=1` 保持注释。
- 模拟服务 PID `15948` 正常监听 `192.168.1.1:8088`，日志为
  `interface=eth1 real=false`。
- `/healthz` 返回 `ok=true`、`real_control=false`；状态返回
  `capability_check=simulation`；模拟开启、关闭和 H5 HTTP 200 均通过。
- 模拟 API 操作前后，真实 `eth1` 都保持管理 UP、无载波、
  `operstate=down`、`carrier=0`，确认未修改 LAN2。
- 已删除 `/var` 的两个上传临时文件和持久目录中的 GOARM=7 不兼容副本；
  这些设备文件不可恢复，但可从本地源码重新构建。
- 未重启光猫，未启用真实模式，未执行真实端口开关。
