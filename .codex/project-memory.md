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
- 系统不会遍历执行启动脚本目录。OpenWrt 的
  `/opt/cu/apps/apps/etc/init.d/done` 在 `START=95` 的 `boot()` 中只调用
  固定入口 `/root/scripts/bootshell.sh`；自定义服务必须由
  `bootshell.sh` 显式启动。
- **部署硬约束：需要以 OpenWrt/LXC 容器进程运行的服务，只能通过修改持久
  固定入口 `bootshell.sh` 后重启光猫来启动。Telnet 位于主系统视角；通过
  Telnet 直接启动的进程永远不等同于可长期部署的容器进程，也不能作为容器
  启动或运行验收结果。**
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
  bootshell.sh                  光猫固定启动入口模板
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
  - `POST /api/v1/limiter`，运行时设置是否启用及单次最长观看分钟数
  - `GET /healthz`
- 真实模式通过配置的 `ip link set dev <iface> up|down` 控制接口；程序默认
  为 `/bin/ip`，SG631Z 容器部署必须显式配置为 `/sbin/ip`。
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
- 提供智能限时启用开关、单次最长观看分钟数输入和保存按钮。
- 显示限时状态及当前已观看分钟数。
- 每 5 秒刷新一次状态。
- 当前没有认证、操作确认或自动恢复倒计时。

### 智能限时核心（开发机验证阶段）

- 已实现 `idle`、`watching`、`intervention_up`、
  `intervention_down` 四状态状态机。
- 载波默认每 30 秒查询；连续观看默认 20 分钟；干预默认循环执行 57 秒 up、
  3 秒 down，均可通过环境变量配置。
- 连续观看使用经过时间计算；自动 down 阶段忽略 `carrier=0`，避免将自身
  动作误判为机顶盒关机。
- 控制动作成功后才提交状态；失败时保留当前状态、记录错误并短间隔重试。
- 手动 API 动作优先，会清除自动计时和动作定时器。
- 服务正常停止且正处于自动 down 阶段时，会尽力恢复接口为 up。
- 状态 API 已附带 `limiter` 快照；自动功能默认关闭。
- 已实现 CRC 追加日志持久化、5 分钟进度检查点、30 秒配置写合并、64 KiB
  原子压缩，以及重启后的累计时间/干预 up 阶段恢复。

### 构建和部署材料

- `Makefile` 提供 `test`、`run`、`build-arm`。
- Go 缓存可放在项目 `.cache` 下，`.cache` 和 `dist` 已被忽略。
- `deploy/bootshell.sh` 是光猫固定启动入口模板，使用容器路径启动 cpolar 和
  IPTV 真实服务，并将状态日志放在 `$BASE/state/state.log`。
- `README.md` 提供本地开发、设备模拟、设备实际三种模式的快速对照。
- `docs/运行与部署.md` 提供从本地开发到 ARMv7 构建、设备模拟验证、实际
  模式切换、API 调用和故障排查的完整步骤。

## 尚未实现或尚未完成

以下内容写在阶段计划中，但当前代码尚未实现或尚未完成验证：

- 定期读取、状态对账和厂商进程回拉检测；
- 完整启动能力检查（namespace、CAP_NET_ADMIN、`/bin/ip` 可执行性等）；
- 设备重启后服务自动运行验证；
- 设备端 HTTP API/H5 的端到端真实开关验证；
- 观察 1 至 5 分钟以确认厂商进程是否会自动回拉接口。

## 已知实现问题和风险

1. 用户明确接受自动 down 期间进程崩溃需要重启光猫恢复的风险，不实现进程外
   延迟 up 兜底。持久配置已在设备写入成功，但累计进度跨每日重启和异常断电
   仍需长期观察。
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

- 重启前，模拟服务 PID 为 `15948`，监听 `192.168.1.1:8088`。
- 日志明确显示 `real=false`，进程环境中没有 `IPTV_CONTROL_REAL`。
- `/healthz`、状态 API、模拟开关 API 和 H5 首页均验证通过。
- 服务已安装到持久目录。
- 用户随后重启设备，验证发现服务没有自动运行。根因是
  `iptv-control-start.sh` 没有被固定入口 `bootshell.sh` 引用，不是程序
  或持久文件损坏。

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

进入第二阶段“智能限时”，详细状态机、默认参数、实现任务和安全约束见
`.codex/PLAN.md`。优先实现可测试的状态机与配置，再补持久化、API/H5，
最后使用缩短参数做设备实机验证。

## 当前任务

任务（2026-07-24）：诊断智能限时新版本在用户确认机顶盒已关闭时仍报告
`carrier=1`、`operstate=unknown` 的问题；只读对比 API、主系统与容器 sysfs、
桥接和物理链路状态，不修改配置、不操作 LAN2。

状态：进行中。

---

任务（2026-07-24）：用户接受程序崩溃时通过重启光猫恢复 LAN2 的风险，明确
不实现每次自动 down 的进程外延迟 up 兜底；将当前智能限时版本部署到真实
设备并完成重启运行验收。

状态：已完成真实设备部署与基础验收。

部署边界：保持 `IPTV_CONTROL_REAL=1`，智能限时初始默认关闭，由用户在 H5
确认配置后启用；上传前备份设备现有二进制和启动脚本，必须通过容器根视角
校验后重启，重启后验收真实状态 API、H5 和持久状态路径。

完成结果（2026-07-24）：

- 本地 GOARM=5 构建与测试通过；部署二进制 SHA-256：
  `1263dfabe412e29c09af63e9f37f63d8521790e0e570b4cb8b28599fb47d936c`，
  启动脚本 SHA-256：
  `6423ec580db10f95709c232c765e4c21008f23badc72f46175e9ad6529a05e11`。
- 设备旧二进制以硬链接
  `iptv-control.pre-smart-20260724` 保留，旧启动脚本保存为
  `bootshell.sh.pre-smart-20260724`。
- 新文件在主系统持久路径完成双哈希、权限和脚本语法校验；容器重启前因
  overlay dentry 缓存仍看到旧二进制 inode，重启重新挂载后新版本 API 的
  `limiter` 字段确认新二进制已经运行。
- 重启后 `/healthz` 返回 `real_control=true`；状态为
  `admin_up=true`、`carrier=1`，智能限时默认关闭、最长 20 分钟。
- H5 已确认包含智能限时开关、最长观看时间输入和保存按钮。
- 通过真实设备 API 保存“关闭、20 分钟”，`persistence_pending` 随后消失，
  表明 `$BASE/state/state.log` 已同步写入成功。
- 重启后 Telnet 自动关闭，符合设备既有特性；未通过 Telnet再次读取进程哈希，
  也未启用智能限时或主动执行自动 down。
- 持久卷部署后剩余约 16.7 MiB。
- 最终复查时，用户已通过页面将智能限时设为启用、40 分钟；API 返回
  `state=watching`、`admin_up=true`、`carrier=1`。该配置由用户操作，部署
  过程未覆盖。

---

任务（2026-07-24）：明确智能限时干预期间家长手动开关的优先级，以及光猫
每日自动重启后的配置与运行状态持久化语义；用户确认方案后再修改代码。

状态：已完成开发机实现与验证。

待决策：

- 家长在 `intervention_up` / `intervention_down` 阶段手动开启或关闭 LAN2
  时，是仅临时覆盖、结束本轮限时，还是需要显式的“暂停智能限时”操作。
- 持久化仅保存启用开关和最长观看时间，还是同时保存观看计时与干预状态。
- 重启发生在自动 down 阶段时，启动后必须优先恢复 LAN2 为 up，不能直接
  恢复 down 状态。

用户决策（2026-07-24）：

- 手动关闭采用方案 A：立即关闭 LAN2，并退出智能限时。
- 手动开启采用方案 A：若智能限时原本启用，则重新获得完整观看时长；若原本
  关闭，手动开启不能自动启用智能限时。
- 不增加额外家长操作按钮。
- 重启采用方案 B：持久化配置和累计观看时间；启动后根据载波恢复计时，但
  绝不直接恢复为 down。
- 持久化方案必须结合 SG631Z 的闪存芯片、主控、UBI/overlay 和实际持久路径
  评估，目标是闪存至少正常工作两年。

实机存储调查（2026-07-24，只读）：

- 主控：ZTE ZX279127S，ARM Cortex-A9（CPU part `0xc09`）。
- raw NAND 总容量 256 MiB，页大小 2,048 字节，物理擦除块 131,072 字节。
- 持久应用分区为 `mtd12 apps`，大小 62 MiB，经 UBI1/UBIFS 挂载到
  `/opt/cu/apps`；容器对应 `/opt/apps`，挂载参数包含 `rw,sync`。
- UBI1：494 个物理擦除块、2 个当前坏块、38 个坏块预留、`max_ec=56`。
- 应用卷：452 个逻辑擦除块，每块 126,976 字节，卷数据容量 57,393,152
  字节。
- 内核没有暴露 NAND 颗粒的厂商/型号及 bits-per-cell，不能声称确定的 P/E
  规格；寿命方案按较差的 MLC NAND 下限估算。

推荐持久化方案：

- 采用 CRC 保护的追加式小记录日志，而不是每次覆盖 JSON；5 分钟观看进度
  检查点按 2 KiB NAND 页计，两年持续写入约 420 MiB，仅约 7.3 个应用卷
  容量，之后仍由 UBI 跨擦除块磨损均衡。
- 配置写入做 30 秒合并，进度每 5 分钟写一次；进入干预、正常停止时强制
  刷新，57/3 秒动作循环不写闪存。
- 日志到 64 KiB 后原子压缩；启动扫描最后一条有效 CRC 记录，容忍断电残尾。
- 正常每日重启通过 SIGTERM 刷新时不丢进度；非正常断电最多回退约 5 分钟。
- 该方案相较每分钟原子替换文件显著降低 UBIFS 数据页和元数据写入，在颗粒
  型号未知的情况下为“两年正常使用”保留充足余量，但不能对未知颗粒作绝对
  硬件寿命保证。

实现结果（2026-07-24）：

- 新增 `state_store.go`：追加式 JSON 记录、CRC32、递增序号、`fsync`、残尾
  容错和 64 KiB 原子压缩。
- 智能限时配置、累计观看秒数和阶段可恢复；持久的 down 阶段统一恢复为
  `intervention_up`。
- 观看进度每 5 分钟请求检查点；配置写在 30 秒内合并；进入干预和正常停止
  强制刷新；相同状态不会重复写。
- 手动关闭/开启均清除本轮进度但保留智能限时配置，因此开启后仅在原配置启用
  时重新获得完整时长。
- `deploy/bootshell.sh` 已设置
  `IPTV_CONTROL_STATE_FILE="$BASE/state/state.log"`。
- 开发机普通测试、竞态测试、静态检查、GOARM=5 构建全部通过；进程级跨重启
  测试确认页面/API 保存启用和 25 分钟后，第二个进程在环境默认关闭的情况下
  仍恢复为启用和 25 分钟。状态日志首条记录为 178 字节。
- 本轮当时未部署光猫；用户随后明确接受不实现进程外延迟 up 兜底的风险，
  当前版本已完成真实设备部署。

---

任务（2026-07-24）：增加前端智能限时控制能力，支持运行时启用/停用智能限时，
以及设置单次最长看电视时间；通过开发机测试验证 API、状态机和 H5。

状态：已完成。

---

任务（2026-07-24）：增加手动触发的 GitHub Actions Release 工作流；运行后
自动发布由 Actions run number 决定版本的 Release，附件包含 SG631Z 可部署
二进制和 `bootshell.sh`。

状态：进行中，先检查仓库现有工作流和发布约定；对影响 tag/产物格式的不确定
项向用户确认后实现。

完成结果（2026-07-24）：

- 用户确认发布格式：
  - Tag：`v<github.run_number>`；
  - 标题：`IPTVController v<github.run_number>`；
  - 两个独立附件：`iptv-control`、`bootshell.sh`；
  - 正式 Release，自动生成 Release Notes。
- 新增 `.github/workflows/release.yml`：
  - 唯一触发器为 `workflow_dispatch`，不会响应 push/tag；
  - 最小权限为 `contents: write`；
  - 使用 `actions/checkout@v4`、`actions/setup-go@v5`；
  - 先执行 `go test ./...`；
  - 固定 `GOOS=linux`、`GOARCH=arm`、`GOARM=5`、`CGO_ENABLED=0`，
    使用 `-trimpath -ldflags="-s -w"` 构建设备可部署二进制；
  - 将 `deploy/bootshell.sh` 以 0755 权限复制为 Release 附件；
  - 使用 GitHub Runner 自带官方 `gh release create` 和当前
    `github.token` 创建 Tag/正式 Release，设为 latest 并生成说明。
- README 已增加手动触发、版本规则、附件和同一 run 重新运行会因版本已存在
  而失败的说明。
- 本地验证通过：`go test ./...`、相同参数 GOARM=5 构建、PyYAML BaseLoader
  解析和结构断言、`git diff --check`；产物名称和文件大小检查通过。
- 本轮只新增工作流并做本地验证，未实际触发 GitHub Actions 或创建 Release。

状态：已完成。

---

任务（2026-07-24）：修复儿童通过短暂拔网线绕过智能限时；增加 30 分钟
冷却锁定、观看期间家长手动进入干预，以及修改观看/阻断参数不清空当前状态。
本地逻辑验证通过后直接部署光猫。

状态：进行中。

确认的状态机语义：

- 新增 `cooldown`，固定持续 30 分钟并保持 LAN2 管理 down；到期后自动 up，
  清空累计观看时间并回到 `idle`。
- 已进入 `watching` 后，只要接口仍由自动逻辑管理且 DBus 载波变 0，即使尚未
  达到最大观看时间，也保留累计时间并立即进入 cooldown；这覆盖儿童关机或
  拔线逃避计时。
- `intervention_up` 中载波变 0 不再回 idle，而是进入 cooldown。
  `intervention_down` 因自动 down 无法判断物理拔线；恢复 up 后下一次有效
  采样若仍为 0，再进入 cooldown。
- cooldown 状态和到期时间写入持久日志；重启后若未到期必须重新 down LAN2，
  不能通过重启绕过。
- 新增“立即干预”操作，仅当状态为 `watching` 时服务端允许，进入
  `intervention_up`；H5 按状态启用按钮。
- 智能限时已启用时修改最大观看时间或阻断秒数，只更新参数，保留累计时间、
  当前状态和动作定时器；真正停用时才恢复自动 down/cooldown 中的端口并重置。
- 现有家长手动 LAN2 开关继续作为显式覆盖，保持此前确认的语义。

本地实现进展：

- 新增 `LimiterCooldown`、固定 30 分钟 cooldown、截止时间持久化和启动恢复
  端口协调；冷却期间 down/up 失败按短间隔重试但不延长原截止时间。
- watching 或 intervention_up 检测到载波消失时，保留累计时间并立即 down；
  cooldown 到期成功 up 后才清空并回 idle。
- 新增 `POST /api/v1/intervene` 和 H5“立即进入干预”按钮，仅 watching 可用。
- 已启用状态下修改最大观看时长/阻断时长会保留 watching、
  intervention_up、intervention_down 和 cooldown 的状态、累计时间及当前
  动作截止时间；停用才恢复并重置。
- 旧持久记录继续兼容；cooldown 的到期时间新增为尾部 `omitempty` 字段，
  不破坏旧 JSON/CRC。
- 专项测试覆盖提前关机、干预拔线、冷却强制 down、down/up 重试、30 分钟
  到期、冷却跨重启/过期释放、强制持久化、手动干预权限、各活动状态无损
  参数修改和启动端口协调。
- 本地验证通过：`go test ./...`、`go test -race ./...`、`go vet ./...`、
  `git diff --check` 和 GOARM=5 交叉构建。
- 待部署二进制 SHA-256：
  `775a04194f073928b43038fd7bdf2fb31fb4d257aff5989ecd8ad20b1e001221`。

完成结果（2026-07-24）：

- 状态机最终实现：
  - watching 中载波消失，无论是否达到观看上限，都保留累计时间并立即 down，
    进入固定 30 分钟 cooldown；
  - intervention_up 中拔线同样进入 cooldown；intervention_down 忽略自动
    down 造成的载波 0，恢复后再根据有效采样判断；
  - cooldown 未到期时强制保持 down；到期 up 成功后才清空累计并回 idle；
  - down/up 失败使用动作重试时间，但 `cooldown_until` 始终保持原截止点。
- cooldown 的 phase、累计时间和 UTC 截止时间写入带 CRC 的状态日志；服务
  停止前强制刷新。重启恢复未到期 cooldown 时显式 down，已过期时显式 up。
- 新增 `POST /api/v1/intervene`，只允许 watching；H5 按状态启用“立即进入
  干预”按钮，并显示 cooldown 预计剩余分钟。
- enabled 保持 true 时修改最大观看时间或阻断秒数只更新配置，保留
  watching/intervention_up/intervention_down/cooldown 的累计、状态和当前
  定时；停用才恢复端口并重置。
- 本地专项及全量验证通过：
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - `git diff --check`
  - GOARM=5 交叉构建
- 新二进制上传后设备端 SHA-256 匹配，持久文件原子替换，并严格通过
  `sync; reboot` 由容器开机脚本启动；未用 Telnet 启动替代进程。
- 重启验收：
  - 8088、H5 和 DBus 状态源正常；
  - H5 包含新按钮，`/api/v1/intervene` 已注册；
  - 原配置完整恢复为 enabled、20 分钟、阻断 15 秒；
  - 机顶盒关闭时 `admin_up=true`、`carrier=0`、state=idle；
  - idle 状态 POST 手动干预返回 HTTP 409，状态和端口不变；
  - 容器进程 PID 2682，`/proc/2682/exe` 与容器持久二进制哈希均为
    `775a04194f073928b43038fd7bdf2fb31fb4d257aff5989ecd8ad20b1e001221`。
- 部署时临时创建的旧版回滚副本已在验收成功后删除；设备目录只保留当前
  `iptv-control`，apps 分区仍为 55% 使用、20.8 MiB 可用。
- 按用户要求，本轮 cooldown 的完整 30 分钟行为只做本地加速逻辑测试；
  设备端仅做启动、兼容配置、API 权限和无副作用状态验收。

状态：已完成。

---

任务（2026-07-24）：下一版允许家长设置智能干预每分钟的阻断时长，范围
1–25 秒；本地验证通过后直接上传光猫并重启。

状态：进行中。

实施边界：

- 默认值保持 6 秒，总周期保持 60 秒；接通时长自动为 `60-阻断秒数`。
- H5、API、状态快照和持久化同时增加该配置；旧状态记录缺少字段时按 6 秒
  兼容恢复。
- 服务端必须独立校验 1–25 秒，不能只依赖前端输入限制。
- 先完成普通测试、竞态测试、静态检查和 GOARM=5 构建；全部通过后按持久
  `bootshell.sh` 加光猫重启的唯一部署路径更新，不用 Telnet 启动替代进程。

部署进展：

- 本地验证全部通过：`go test ./...`、`go test -race ./...`、`go vet ./...`、
  `git diff --check` 和 GOARM=5 交叉构建。
- 新二进制 SHA-256：
  `784d3f2cd71ed879dd80c3540b948bffb9c8fc93134e8910ab5273b9c2e37dd0`。
- 已通过 Telnet Base64 上传并校验；替换前备份当前 DBus 版本为
  `iptv-control.pre-block-config-20260724-1635`，随后原子替换并执行
  `sync; reboot`。
- 重启后 API 已出现 `block_seconds`，旧持久记录兼容恢复成功。用户已完成
  功能测试并确认通过；当前配置为智能限时启用、20 分钟、阻断 15 秒。
- 用户随后明确要求清理设备上的旧版二进制和旧启动脚本。清理前将先列出精确
  备份文件，只删除备份后缀，保留当前文件、状态日志和其他应用文件。

完成结果（2026-07-24）：

- 功能实现：
  - H5 新增“每分钟阻断时长（秒）”，前端范围 1–25、默认 6；
  - `POST /api/v1/limiter` 新增可选 `block_seconds`，服务端独立校验 1–25
    整数；旧客户端省略时保留当前值；
  - 状态快照新增 `block_seconds`；
  - 状态机接通时长自动使用 `60-block_seconds`，阻断时长使用用户设置；
  - 持久记录新增 `down_duration_seconds`，使用 `omitempty` 保持旧版 JSON/CRC
    记录可读取；旧记录缺失字段时恢复默认 6 秒。
- 测试覆盖边界 1/25、非法 0/26/小数秒、旧 API 兼容、旧 CRC 日志兼容、
  新配置持久化和恢复；普通测试、竞态测试、`go vet`、`git diff --check`
  及 GOARM=5 构建全部通过。
- 新二进制 SHA-256：
  `784d3f2cd71ed879dd80c3540b948bffb9c8fc93134e8910ab5273b9c2e37dd0`。
- 新二进制已上传、校验、原子替换并通过 `sync; reboot` 由容器启动。重启后
  API 正确返回 `block_seconds`，用户完成实际测试并确认通过；当前配置为
  智能限时启用、20 分钟、阻断 15 秒。
- 按用户明确要求清理设备旧文件，已删除：
  - 3 个旧二进制备份：
    `pre-block-config-20260724-1635`、`pre-smart-20260724`、
    `pre-dbus-20260724-1608`；
  - 5 个旧启动脚本备份：
    `pre-smart-20260724`、`real-bin-ip-20260724`、
    `pre-dbus-20260724-1608`、`simulation-20260724`、`bootshell.sh.prev`。
- 清理后只剩当前 `iptv-control` 和当前 `bootshell.sh`，哈希分别仍为
  `784d3f...37dd0` 与 `50b426...27df`；状态日志和其他应用文件未动。
  apps 分区占用从 80% 降为 55%，可用空间从 9.2 MiB 增加到 20.9 MiB。
- 旧备份已从设备永久删除，无法在设备端直接恢复；本地源码和交叉构建能力
  保留。

状态：已完成。

---

任务（2026-07-24）：将 DBus `LAN2Status`/54 秒接通 6 秒阻断版本部署到
SG631Z，重启光猫并进行真实运行验证。

状态：进行中。

执行约束与步骤：

- 只替换持久应用目录和持久 `bootshell.sh`；长期进程必须由容器开机脚本在
  光猫重启后产生，绝不通过 Telnet 临时启动程序代替部署。
- 替换前保留当前二进制和启动脚本备份；上传后同时从主系统持久路径和运行中
  容器根视角核对 SHA-256，并检查脚本语法。
- 重启后验证 8088 自动恢复、进程哈希、`real_control=true`、
  `carrier_source=dbus_lan2_status`、DBus 载波值和持久限时配置。
- 若重启后 LAN2 物理载波为 1，记录原智能限时配置，临时设置为 1 分钟，
  观察一次 54 秒 up/6 秒 down 的真实周期后恢复原配置；若载波为 0，则先
  完成部署与状态源验收，不盲目触发真实干预。

完成结果（2026-07-24）：

- 本地重新构建 GOARM=5 二进制：
  `fa072a8be370011d5dd7c3f5b2f2d0a8ae7f549b3157073c0dbc36ee3bfa4371`；
  新 `bootshell.sh`：
  `50b426242cd711bed0e0de74015a08b62ca94b646002a0539a4beaad20fa27df`。
- FTP 登录可用但上传被设备主动重置，未留下临时残片；改用 Telnet 关闭回显
  后的 Base64 流式上传，设备 `/tmp` 中的文件大小和 SHA-256 均完全匹配。
- 替换前保留备份：
  - `/opt/cu/apps/apps/opt/apps/iptv-control/iptv-control.pre-dbus-20260724-1608`
  - `/opt/cu/apps/apps/root/scripts/bootshell.sh.pre-dbus-20260724-1608`
- 持久路径已原子替换并通过哈希、权限和 shell 语法校验。运行中容器因已执行
  文件 inode 缓存仍看到旧二进制，未用 Telnet 临时启动替代进程。
- 执行 `sync; reboot` 后由固定容器开机脚本启动新进程。实测：
  - 系统 uptime 约 3 分钟，证明发生了真实重启；
  - 新进程 PID 3164，`/proc/3164/exe` 和容器文件哈希均为新二进制哈希；
  - 容器启动脚本哈希匹配，环境包含
    `IPTV_CONTROL_REAL=1`、`IPTV_CONTROL_IP=/sbin/ip`、
    `IPTV_CONTROL_DBUS_SEND=/usr/bin/dbus-send` 和持久状态文件路径；
  - `/healthz` 返回 `ok=true`、`real_control=true`。
- DBus 状态源实测：
  - 重启后机顶盒关闭：页面显示管理 UP、`operstate=unknown`、
    `carrier=0 (dbus_lan2_status)`，限时保持 idle；
  - 机顶盒开启：API 返回 `carrier=1`、来源为 DBus，进入 watching。
- 原配置记录为智能限时启用、5 分钟。真实周期测试临时改为启用、1 分钟：
  - 进入 watching 后累计到限时；
  - 16:14:49 进入 `intervention_up`，下次 down 计划为 16:15:42；
  - 用户在电视端确认实际表现正常；后续 API 显示 LAN2 已自动恢复为
    `admin_up=true`、`carrier=1`，`last_change` 记录了自动 up 动作。
- 已停止额外监控，并通过 API 恢复原配置“启用、5 分钟”；恢复响应为
  `state=idle`、`admin_up=true`、`carrier=1`，从下一轮 DBus 采样重新获得
  完整 5 分钟时长。持久化请求已进入正常合并写队列。

状态：已完成。

本轮边界：设置保存在进程内存中，服务重启后仍回到环境变量默认值；暂不部署
设备。停用智能限时时必须取消自动计时，并在自动 down 状态下先恢复接口。

完成结果（2026-07-24）：

- H5 已增加智能限时启用开关、1 至 1440 分钟的单次最长观看时间输入及运行
  状态展示。
- 新增 `POST /api/v1/limiter`；设置立即生效并重新开始本次自动计时。
- 修正运行循环，使服务启动时即使智能限时默认关闭，之后也可从页面动态启用。
- 停用设置时若处于自动 down，会先恢复 LAN2；恢复失败则拒绝配置变更并返回
  错误。
- 新增 API、页面元素、运行时启用、配置更新和停用恢复测试。
- 开发机验证通过：普通测试、竞态测试、`go vet`、GOARM=5 构建，以及本地
  HTTP 进程级模拟（默认 20 分钟，页面修改为 25 分钟后即时生效）。
- 设置尚未持久化，本轮未部署设备。

---

任务（2026-07-24）：先明确智能限时状态机和状态转移关系，再编写第二阶段
核心代码；必须先在开发机通过模拟/单元测试，确认逻辑后才考虑设备部署。

状态：已完成。

本轮边界：实现可注入时间的纯状态机、自动控制循环和配置解析，覆盖连续观看、
达到限时、57/3 秒干预、自动 down 期间忽略载波、机顶盒关机退出、控制错误和
安全停止等路径。自动功能默认关闭，不修改或部署设备端二进制。

完成结果（2026-07-24）：

- `.codex/PLAN.md` 已补充完整状态转移表、不变量和手动优先关系。
- 新增 `limiter.go`，实现配置校验、四状态状态机、30 秒载波轮询和独立的
  57/3 秒动作定时。
- 新增 `limiter_test.go`，覆盖主要状态转移、关机退出、自动 down 载波忽略、
  动作失败重试、停止恢复、手动覆盖、配置校验和加速运行循环。
- `GET /api/v1/status` 已返回限时状态快照；功能默认关闭。
- 开发机验证通过：`go test ./...`、`go test -race ./...`、`go vet ./...`、
  GOARM=5 交叉构建，以及本地进程级 HTTP 模拟。
- 本轮未部署设备。设备部署前仍需实现进程外延迟恢复兜底、状态持久化和 H5
  配置/展示，并继续评审异常断电与重启恢复语义。

---

任务（2026-07-24）：电视已经启动，继续此前暂停的能力诊断，并使用
`docs/credential.json` 中结构化凭据连接设备，完成设备实际模式的全真运行
验证。

状态：已完成。

验证边界：允许在确认管理连接不依赖 LAN2、真实状态读取正常且延迟恢复兜底
已经启动后，设置 `IPTV_CONTROL_REAL=1` 并通过 HTTP API 对 `eth1` 执行一次
短时 down/up；必须确认接口恢复、电视业务产生预期短暂断连且服务仍可访问。
凭据只用于连接，不复制到代码、日志或本记忆文件。

执行纠正（2026-07-24）：尝试从 Telnet 主系统 shell 使用容器路径重启服务，
旧模拟进程已停止，但 `/opt/apps/...` 在主系统视角不可见，新进程未启动。
后续不再通过 Telnet 启动替代进程；按部署硬约束修改持久
`bootshell.sh`、校验后重启光猫，由固定入口产生真实容器进程。

完成结果（2026-07-24）：

- root、能力位、`eth1`、容器视角脚本和二进制检查通过。
- 固定入口已启用 `IPTV_CONTROL_REAL=1`，并修正
  `IPTV_CONTROL_IP=/sbin/ip`；真实重启后 `/healthz` 返回
  `real_control=true`，H5 和状态 API 可用。
- 发现并纠正了主系统 `/bin/ip` 与容器 `/sbin/ip` 的路径差异；不能用
  Telnet 主系统的工具检查结果替代容器视角检查。
- 用户已手动完成真实开关验收，确认 API 能正确开启和关闭 LAN2。
- 载波语义经实机确认：机顶盒开机且 LAN2 启用时 `carrier=1`，机顶盒关机
  或 LAN2 关闭时 `carrier=0`。
- 第一阶段完成；下一任务为可配置的智能限时功能。

2026-07-24 继续执行：重新上传 `deploy/bootshell.sh` 后，将同时通过主系统
持久路径和运行中容器进程的 `/proc/<pid>/root` 视角验证文件与二进制。只有
容器视角确认后才重启；重启验收以 8088/API 实际可用为准，而不是仅依赖
主系统视角的文件校验。

实施计划：

- 本地部署模板改为单一 `deploy/bootshell.sh`，使用容器路径
  `/opt/apps/iptv-control`，cpolar 和 IPTV 服务均后台启动。
- 上传并校验新的 `bootshell.sh`，替换前保留当前文件备份。
- 删除设备上的 `/root/scripts/iptv-control-start.sh`。
- 重启光猫，等待系统和网络恢复；Telnet 需由用户重启后重新开启时按实际
  可用状态检查。
- 验证 cpolar/IPTV 进程、8088、日志、H5/API 和 `real_control=false`；
  不操作真实 LAN2。

完成结果（2026-07-24）：

- 仓库部署模板已改为单一 `deploy/bootshell.sh`，删除
  `deploy/iptv-control-start.sh`。
- 设备上的 `/root/scripts/iptv-control-start.sh` 已删除；原
  `bootshell.sh` 保留为 `bootshell.sh.prev`。
- 新 `bootshell.sh` 使用容器路径 `/opt/apps/iptv-control`，cpolar 和
  IPTV 服务均后台启动，`IPTV_CONTROL_REAL=1` 保持注释。
- 重新上传后，已通过 cpolar 容器进程的 `/proc/<pid>/root` 视角确认：
  - `/root/scripts/bootshell.sh` SHA-256 为
    `f8698ad534c2865117e952b3b2907fba631df54e9d4bf1ece7d4375e557bf060`；
  - `/opt/apps/iptv-control/iptv-control` SHA-256 为
    `202ff3c4fdcf062ffacd468a0deb94e3cbc56f15ecc773cf8867a2fa1a64b75b`；
  - 容器根目录下 `sh -n /root/scripts/bootshell.sh` 返回 0。
- 已执行 `sync; reboot`。重启后不依赖 Telnet，8088 自动恢复：
  - `/healthz` 返回 `ok=true`、`real_control=false`；
  - `/api/v1/status` 返回 `capability_check=simulation`；
  - H5 首页返回 HTTP 200。
- 结论：单一 `bootshell.sh` 开机自启链已经通过真实重启验证，
  服务当前保持设备模拟模式，未操作真实 LAN2。

---

任务（2026-07-24）：诊断智能限时部署后机顶盒关机时 API 仍报告
`carrier=1`、`operstate=unknown` 的实机状态源问题。

状态：已完成诊断，尚未修改代码或重新部署。

诊断结果：

- 用户提供的联通管理页显示 LAN2 未连接时，主系统、容器和 API 曾同时返回
  `eth1 carrier=1`、`operstate=unknown`；本固件 Linux netdev 在启动后的
  初始状态并不可靠，不能作为智能限时的唯一物理链路来源。
- 厂商调试节点确认物理 LAN2 对应 switch port 1：关机为 `link=0`，开机为
  `link=1, 100M, full duplex`。该节点每次查询都会写内核日志，不适合
  30 秒周期轮询。
- 找到厂商正式只读 DBus 属性：
  - service: `com.cuc.igd1`
  - path: `/com/cuc/igd1/Info/Network`
  - interface: `com.cuc.igd1.NetworkInfo`
  - property: `LAN2Status`（byte）
- 实机 A/B 验证：机顶盒开机时该属性返回 `byte 1`，关机时返回 `byte 0`；
  主系统和 IPTV 容器结果一致，并与厂商页面、交换芯片状态一致。
- IPTV 容器已有 `/usr/bin/dbus-send` 且挂载 system bus socket，因此可以
  无 Web 登录、无新增凭据、无内核日志污染地读取 `LAN2Status`。
- 关机后的本次 sysfs 也变为 `carrier=0`、`operstate=down`，说明 sysfs
  会在后续物理事件中更新，但不能消除启动初始状态错误。建议真实设备改用
  DBus `LAN2Status`，sysfs 仅保留为开发机模拟或兼容回退；等待用户授权后
  再实现、测试和部署。

---

任务（2026-07-24）：按实机诊断结果重写载波读取，真实设备改用厂商 DBus
`LAN2Status`；同时将智能干预从 57 秒接通/3 秒阻断调整为
54 秒接通/6 秒阻断。

状态：进行中。

实施边界：

- DBus 查询与输出解析必须可在开发机通过假命令完整模拟测试。
- 真实模式下 DBus 查询失败必须作为读取错误处理，不能回退到已证实启动初态
  不可靠的 sysfs，也不能误算为正在观看。
- 模拟模式保留现有 sysfs 载波来源。
- 完成代码、文档和开发机测试后，再决定实机部署步骤。

完成结果（2026-07-24）：

- 真实模式的 `PortStatus.carrier` 已改读厂商 DBus
  `com.cuc.igd1.NetworkInfo.LAN2Status`；新增 `carrier_source`，真实设备
  返回 `dbus_lan2_status`，模拟模式返回 `simulation`。
- Linux sysfs 仍用于读取接口管理 flags 和诊断用 `operstate`，不再读取
  sysfs carrier。DBus 查询失败直接返回状态错误，不做 sysfs 回退，限时
  runner 也不会在读取失败时推进观看计时。
- DBus 使用容器内 `/usr/bin/dbus-send`，设置 3000 ms reply timeout；
  路径可由 `IPTV_CONTROL_DBUS_SEND` 配置。`deploy/bootshell.sh` 已固定该
  设备路径。
- 在运行中 IPTV 容器内以程序完全相同的参数进行只读查询，命令退出码为 0，
  证明旧固件支持 reply timeout 参数及该 DBus 调用。
- 智能干预默认周期已调整为 54 秒接通、6 秒阻断；H5、README、PLAN 和设备
  文档均同步更新，页面文案纳入自动测试。
- 新增测试覆盖 DBus 开/关输出解析、异常/歧义输出、命令失败、3 秒超时参数，
  以及 sysfs carrier 与 DBus 矛盾时绝不回退；新增默认 54/6 周期断言。
- 开发机验证通过：
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - `git diff --check`
  - `GOOS=linux GOARCH=arm GOARM=5 CGO_ENABLED=0` 交叉构建
- 交叉构建测试产物位于 `.cache/iptv-control-linux-arm`，SHA-256 为
  `fa072a8be370011d5dd7c3f5b2f2d0a8ae7f549b3157073c0dbc36ee3bfa4371`。
- 本任务未替换设备二进制、未修改设备持久启动脚本、未重启光猫。

状态：已完成。
