---
icon: material/new-box
---

!!! question "mill-box 专有协议（上游 sing-box 没有）"

`uss` 是一个**纯 UDP** 的代理协议：所有报文走同一个 UDP 端口，靠预共享密钥（PSK）加密与认证，TCP 流量套一层 KCP + smux 多路复用，UDP 流量走不可靠数据报并在需要时做应用层分片。没有握手、没有证书、不需要域名。

- 线格式：`[实例 ID 8B][计数器 8B][ChaCha20-Poly1305 密文]`，前 16 字节作为 AEAD 的附加认证数据参与认证，**每个报文固定 32 字节开销**
- 密钥：用 32 字节 PSK 经一次 HMAC-SHA256（以实例 ID 为盐、按方向加域分隔）派生**每个客户端实例、每个方向**的密钥；实例 ID 在密文外但受认证保护，因此不可伪造、也不会出现 nonce 复用
- 防重放：每个方向一个 RFC 6479 滑动窗口位图，**先认证后查窗口**（否则伪造的计数器能把真客户端的包全部挤掉）
- 抗 MTU 黑洞：内置 PLPMTUD（RFC 8899 思路）——客户端主动二分探测（探测包填充到目标尺寸、对端回 ack），服务端被动学习（观察到达报文尺寸）；探测结果同时约束 KCP 的 `SetMtu` 与分片粒度，**不依赖 ICMP**
- 报文类型（密文内的第一个字节）：`probe` / `probeack` / `kcp` / `udp` / `udpreply` / `ping` / `pong`
- TCP：一条共享的 KCP + smux 会话承载所有连接，每条 TCP 连接只是一条流（首部是 SOCKS 风格目标地址），短连接不付握手代价
- UDP：原生数据报；超过路径 MTU 时按 32 位分片号切片，对端内存里重组（10 秒超时）

### 快速上手

两端填同一个 32 字节 PSK（base64 或 hex 都接受）：

```bash
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

服务端：

```json
{
  "inbounds": [
    {
      "type": "uss",
      "tag": "uss-in",
      "listen": "::",
      "listen_port": 8443,
      "pre_shared_key": "<32 字节密钥>"
    }
  ],
  "outbounds": [
    {
      "type": "direct",
      "tag": "direct"
    }
  ]
}
```

客户端：

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
      "type": "uss",
      "tag": "uss-out",
      "server": "example.com",
      "server_port": 8443,
      "pre_shared_key": "<32 字节密钥>"
    }
  ]
}
```

写完先自检，再启动：

```bash
mill-box check -c config.json
mill-box run -c config.json
```

启动日志会打印**实际生效**的全部参数（`uss inbound: listen=... session_timeout=... mtu_base=...`），核对两边是否一致看这里，不要看配置文件里写了什么。

### 结构

```json
{
  "type": "uss",
  "tag": "uss-in",

  "listen": "::",
  "listen_port": 8443,

  "pre_shared_key": "",
  "udp_timeout": "5m",
  "session_timeout": "10m",
  "handshake_timeout": "10s",
  "max_sessions": 256,
  "max_udp_flows": 512,
  "max_kcp_channels": 32,
  "replay_window_size": 1024,
  "disable_tcp": false,
  "disable_udp": false,
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
    "enabled": false,
    "base": 1280,
    "max": 1500,
    "step": 8,
    "probe_interval": "10m",
    "probe_timeout": "1s",
    "probe_attempts": 2
  }
}
```

### 监听字段

参阅 [监听字段](/configuration/shared/listen/)。

### 字段

#### pre_shared_key

==必填==

预共享密钥，32 字节，base64（RawURL / URL / 标准）或 hex 编码都接受。**两端必须一致**。长度不对会在启动时报错。

这是本协议**唯一的认证手段**：没有握手、没有用户列表，所以拿到 PSK 的任何人都能连上服务端，也无法按用户区分流量或计费。密钥泄露的处置方式是换密钥（所有客户端一起换）。

#### udp_timeout

UDP 流（一个「客户端流 ID + 目标地址」组合）的空闲回收时间，超时后该流被关闭。默认 5 分钟。DNS、QUIC 这类短交互流会因此被及时回收。

#### session_timeout

客户端实例（一个 PSK + 一个实例 ID）的空闲回收时间。默认 10 分钟。超时后该实例的密钥缓存、UDP 流表、可靠通道与重组缓冲一并释放。

#### handshake_timeout

读取可靠通道流首部（目标地址）的超时，默认 10 秒。客户端可以打开一条流却不发数据，这个超时保证它不会长期占住服务端的 goroutine 与流对象；读到地址后 deadline 立即清除，长连接不受影响。

#### max_sessions

同时保留的客户端实例上限，默认 256。达到上限时**淘汰最久未活动的实例**（不是拒绝新连接），被淘汰的实例下次发包会重新建立。

#### max_udp_flows

单个实例内的 UDP 流上限，默认 512。达到上限时新的目标地址直接丢包（`udp_timeout` 到点后自然回收）。

#### max_kcp_channels

单个实例内的可靠通道（KCP 会话）上限，默认 32。客户端正常只用一条；每条通道带一个 KCP 状态机与若干 goroutine，所以必须有上限。达到上限时丢弃新的通道，**只影响发起方自己**（通道表按实例隔离）。

#### replay_window_size

重放窗口的位数，默认 1024（会向上取整到 64 的倍数），最小 64。窗口越大，容忍的乱序/延迟范围越大、内存占用略高。乱序超过窗口的报文会被丢弃。

#### disable_tcp / disable_udp

关掉对应的承载类型。两者都关会在启动时报错。关掉 TCP 后客户端的 `kcp` 报文被丢弃，关掉 UDP 后原生数据报被丢弃——用于「只想跑一种流量」的场景。

#### kcp

KCP（ARQ 层）参数，语义与 kcp-go 一致：

| 字段 | 默认 | 说明 |
|---|---|---|
| `nodelay` | `1` | `1` = 启用 nodelay（不合并小包、立即发送） |
| `interval` | `10` | 内部刷新间隔（毫秒） |
| `resend` | `2` | 快速重传阈值（跳过几次的 ACK 就重传） |
| `nc` | `1` | `1` = 关闭流控，适合代理这类本身有拥塞控制的流量 |
| `window_size` | `512` | 收发窗口（段数）。**只影响 KCP**，smux 的帧长是固定的 32 KB |
| `max_stream_buffer` | `65536` | 单条 smux 流的缓冲上限 |
| `max_receive_buffer` | `4194304` | smux 会话总接收缓冲上限，不得小于 `max_stream_buffer` |
| `keep_alive_interval` | `10s` | smux 心跳间隔 |
| `keep_alive_timeout` | `30s` | smux 心跳超时，不得小于 `keep_alive_interval` |

#### mtu

PLPMTUD 参数。这里的尺寸都指**整个加密数据报（UDP 载荷）的长度**。

| 字段 | 默认 | 说明 |
|---|---|---|
| `enabled` | `false` | 服务端是否也主动探测。默认关闭：客户端探测、服务端从到达报文被动学习，已经足够；打开只用于排查问题 |
| `base` | `1280` | 搜索下界（IPv6 规定的最小不分片长度），也是「什么都没探通」时的兜底值。**永远不会低于它** |
| `max` | `1500` | 搜索上界，也是发送尺寸的硬上限 |
| `step` | `8` | 搜索粒度 |
| `probe_interval` | `10m` | 每轮探测的间隔。每轮都从 `base` 重新二分，所以路由变化（MTU 变小）会在下一轮被发现 |
| `probe_timeout` | `1s` | 单次探测等待 ack 的时间 |
| `probe_attempts` | `2` | 单个尺寸的尝试次数，全部超时才判定「放不下」 |

!!! warning "不要给 uss 出站配 `udp_fragment: true`"

    PLPMTUD 靠**禁止分片**（`IP_PMTUDISC_DO`）来让超限报文被丢弃，从而判定尺寸放不下。打开 `udp_fragment` 后内核会改成「超限就分片」，探测包会全部「成功」，结果是把 MTU 一路探到上限——此时如果中间链路真的更小，大包仍会被分片或被中间设备丢弃。启动日志在两者同时开启时会打 warn。

### 已知边界

- **只有 PSK，没有用户概念**：无法按用户计费/审计，也无法单独吊销某个客户端（换密钥 = 全体更换）
- **没有连接迁移**：客户端的外网 IP/端口变化（手机切换网络）需要重建会话；原生 UDP 流会重建，TCP 流靠 KCP 超时后重连
- **没有流量伪装**：密文是高熵随机数据，没有 TLS/QUIC 之类的形态，抗 DPI 需要靠端口/路径选择，后续版本才会考虑伪装头与端口跳跃
- **未认证报文仍要付一次认证代价**：PSK 方案下服务端必须先派生密钥才能验证任何东西，所以伪造实例 ID 的洪水会带来固定的 CPU 开销（已把密钥派生降到一次 HMAC）；彻底的解法是 cookie/retry 机制，尚未实现
