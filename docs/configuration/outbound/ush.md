---
icon: material/new-box
---

!!! question "mill-box 专有协议（上游 sing-box 没有）"

`ush` 出站把**一条 SSH 连接**同时用于 TCP 与 UDP：TCP 直接开 `direct-tcpip` 通道（与 [ssh 出站](/configuration/outbound/ssh/) 同一条代码路径），UDP 封成 UoT（udp-over-tcp v2）走同一条连接打到服务端的 [ush 入站](/configuration/inbound/ush/)。所以服务端只需要一个 sshd 端口，不必再暴露第二个端口。

- 认证与加密全部沿用 SSH：`password` 或 `private_key` **至少配一种**（都没配时 `check` 直接报错），`host_key` 可用来 pin 服务端主机密钥
- TCP：`direct-tcpip`，目标地址（含域名）原样交给服务端，域名由服务端解析
- UDP：UoT v2，`uot_server` 填**服务端视角**的 UoT 监听地址；服务端必须是 mill-box 的 `ush` 入站 —— 原生 OpenSSH sshd 只会 `connect(SOCK_STREAM)`，解不了 UoT 请求头
- 一条 SSH 连接承载全部流（TCP 通道 + UoT 流），无活动流时的空闲连接由框架回收
- 只支持 v2 UoT：legacy（v1）不发请求头、无法在固定端口上区分版本，配置里填 1 会在启动时被拒

### 快速上手

客户端（`server` 是 sshd，`uot_server` 是服务端上的 ush 入站）：

```json
{
  "inbounds": [
    {
      "type": "socks",
      "tag": "socks-in",
      "listen": "127.0.0.1",
      "listen_port": 1080
    }
  ],
  "outbounds": [
    {
      "type": "ush",
      "tag": "ush-out",
      "server": "example.com",
      "server_port": 22,
      "user": "ush",
      "password": "…",
      "uot_server": "127.0.0.1:10086"
    }
  ]
}
```

服务端见[入站文档](/configuration/inbound/ush/)（sshd + `ush` 入站监听回环）。自检与启动：

```bash
mill-box check -c config.json
mill-box run -c config.json
```

### 结构

```json
{
  "type": "ush",
  "tag": "ush-out",

  "server": "127.0.0.1",
  "server_port": 22,
  "user": "root",
  "password": "",
  "private_key": "",
  "private_key_path": "$HOME/.ssh/id_rsa",
  "private_key_passphrase": "",
  "host_key": [],
  "host_key_algorithms": [],
  "client_version": "SSH-2.0-OpenSSH_7.4p1",
  "cipher": [],
  "mac": [],
  "kex_algorithm": [],

  "uot_server": "127.0.0.1:10086",
  "uot_version": 2,

  ... // 拨号字段
}
```

### 字段

#### uot_server

==必填==

UoT 监听地址，**从服务端视角看**的地址（sshd 去连它，通常是 `127.0.0.1:10086`）。主机与端口都必须写，只写主机名会在 `check` 时报错。服务端在别的机器/容器里时填那台机器的可达地址。

#### uot_version

UoT 协议版本，默认 `2`（等价于省略）。只接受 `2`：填 `1`（legacy）会在启动时报错 —— v1 不发请求头，服务端只能靠客户端连的 magic 地址判断版本，固定端口的入站观察不到。

#### 其余字段

`server`、`server_port`（默认 22）、`user`（默认 `root`）、`password`、`private_key`、`private_key_path`、`private_key_passphrase`、`host_key`、`host_key_algorithms`、`client_version`（默认随机）、`cipher`、`mac`、`kex_algorithm` 的语义与 [ssh 出站](/configuration/outbound/ssh/) **完全相同**（同一套 SSH 客户端配置），其中 `server` 必填、`password` 与 `private_key*` 至少要有一个。

### 拨号字段

参阅 [拨号字段](/configuration/shared/dial/)。

### 已知边界

- **服务端的 UDP 需要 mill-box**：TCP 侧对端可以是原生 OpenSSH（只要账号允许 `direct-tcpip`），UDP 侧必须是 `ush` 入站。想接原生 sshd 的 UDP，得在服务端另跑一个 UoT 监听器并用 `socks` 出站（`udp_over_tcp` + `detour`）串起来
- **没有独立的用户/会话层**：认证与审计都落在 SSH 账号上，`ush` 不提供多用户、流量统计或按用户限速
- **一条 SSH 连接是共享瓶颈**：所有流共用一个 TCP 通道，SSH 的连接级窗口会限制总吞吐；连接断了所有流一起断（框架会重连，但正在传的流不会恢复）
- **UDP 目标由客户端指定**：服务端入站不校验目标，安全边界完全依赖 SSH 账号可信
