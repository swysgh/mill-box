---
icon: material/new-box
---

!!! question "mill-box 专有协议（上游 sing-box 没有）"

出站侧与 [入站](/configuration/inbound/uss/) 对称：同一个 PSK、同一个 UDP 端口、同一套报文格式。完整示例与协议说明见[入站文档](/configuration/inbound/uss/)。

客户端在这里多做两件事：**主动探测路径 MTU**（填充探测包 + 对端 ack，不依赖 ICMP），以及**按需重拨**（套接字因致命读错误失效时只换套接字，KCP 会话与已有流不受影响）。

### 结构

```json
{
  "type": "uss",
  "tag": "uss-out",

  "server": "127.0.0.1",
  "server_port": 8443,

  "pre_shared_key": "",
  "tcp_mode": "kcp",
  "udp_mode": "raw",
  "udp_timeout": "5m",
  "session_timeout": "10m",
  "max_udp_flows": 512,
  "replay_window_size": 1024,
  "ping_interval": "25s",
  "kcp": {
    "nodelay": 1,
    "interval": 10,
    "resend": 2,
    "nc": 1,
    "window_size": 512,
    "max_stream_buffer": 65536,
    "max_receive_buffer": 4194304,
    "keep_alive_interval": "10s",
    "keep_alive_timeout": "30s"
  },
  "mtu": {
    "enabled": true,
    "base": 1280,
    "max": 1500,
    "step": 8,
    "probe_interval": "10m",
    "probe_timeout": "1s",
    "probe_attempts": 2
  }
}
```

### 字段

#### server

==必填==

服务器地址。可以是 IP 或域名（域名会走本机的域名解析策略，与其它出站一致）。

#### server_port

==必填==

服务器端口（UDP）。

#### pre_shared_key

==必填==

预共享密钥，32 字节，base64 或 hex 编码，**必须与服务端一致**。

#### tcp_mode

TCP 流量的承载方式：

| 取值 | 说明 |
|---|---|
| `kcp`（默认） | 走 KCP + smux 可靠通道，抗丢包；所有 TCP 连接共享一条通道，短连接只是开一条流 |
| `none` | 不支持 TCP：该出站只转发 UDP，收到 TCP 请求直接报错（配合 `disable_tcp` 的服务端，或只做 DNS/UDP 中转时用） |

#### udp_mode

UDP 流量的承载方式：

| 取值 | 说明 |
|---|---|
| `raw`（默认） | 原生数据报：不重传、延迟最低，适合 DNS、游戏、QUIC |
| `kcp` | 走 UoT（UDP over TCP）语义，把数据报跑在可靠通道上：弱网下更稳，代价是重传带来的延迟与队头阻塞 |

`udp_mode: kcp` 需要 `tcp_mode: kcp`（它复用可靠通道），两者组合非法时启动报错。

#### udp_timeout

本端 UDP 流（目标地址）的空闲回收时间，默认 5 分钟。

#### session_timeout

本端保留的客户端实例空闲时间。客户端进程内只有一个实例，此项主要用于日志与一致性，默认 10 分钟。

#### max_udp_flows

单个 UDP 会话内的目标地址数量上限，默认 512。达到上限时淘汰最久未活动的目标（新目标仍可用）。

#### replay_window_size

服务端→客户端方向的重放窗口位数，默认 1024。与入站同名项对应。

#### ping_interval

向服务端发送 `ping` 的间隔，默认 25 秒。作用有两个：维持 NAT 映射（原生 UDP 路径没有别的保活机制），以及探测服务端是否还在。连续 3 个周期没有收到任何回应时，会打一条 warn 并**重建可靠通道**（下一次 TCP 连接在新通道上建立），而不是让连接挂在一个死路径上。

#### kcp

KCP 与 smux 参数，语义见[入站的 kcp 一节](/configuration/inbound/uss/#kcp)。

#### mtu

PLPMTUD 参数，语义见[入站的 mtu 一节](/configuration/inbound/uss/#mtu)。**客户端默认开启探测**（`enabled: true`），服务端默认只做被动学习。

!!! warning "不要同时开 `udp_fragment`"

    见[入站的警告](/configuration/inbound/uss/#mtu)：`udp_fragment: true` 会让探测失去意义。

### 拨号字段

参阅 [拨号字段](/configuration/shared/dial/)。
