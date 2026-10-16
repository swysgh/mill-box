---
icon: material/new-box
---

!!! question "mill-box 专有协议（上游 sing-box 没有）"

`ush` 是「SSH 承载」的入站：它本身**没有任何认证**，只是一个裸的 UoT（udp-over-tcp v2）监听器，设计上只该由前面的 sshd 通过 `direct-tcpip` 通道访问。TCP 由 sshd 自己转发，UDP 才需要这个监听器。

- 端口上接受的**每条 TCP 连接都被当作一条 UoT 流**：首部是 `[1B isConnect][目标地址]`，之后是 `[u16 长度][载荷]` 帧（v2 格式）
- 服务端从 UoT 请求里取目标地址，然后按 **UDP 包连接**路由 —— 出站要有 UDP 能力（`direct`、`socks` 等）
- 只支持 v2：legacy UoT（v1）根本不发请求头，靠「客户端连的是哪个 magic 地址」区分版本，固定端口的监听器观察不到这个信息
- 除监听字段外**没有任何参数**：连接生命周期由 sshd 的通道与 UoT 流决定，服务端不做额外超时管理
- 配套的客户端是 [ush 出站](/configuration/outbound/ush/)，它复用同一条 SSH 连接：TCP 走 `direct-tcpip`、UDP 封成 UoT 打到本监听器

### 快速上手

服务端：sshd 在 22 端口，`ush` 监听器只绑回环的 10086（UoT 流由 sshd 转进来）。

```json
{
  "inbounds": [
    {
      "type": "ush",
      "tag": "uot-in",
      "listen": "127.0.0.1",
      "listen_port": 10086
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

客户端：TCP 与 UDP 都走同一个 SSH 端口，`uot_server` 填**服务端视角**的 UoT 监听地址。

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

写完先自检再启动：

```bash
mill-box check -c config.json
mill-box run -c config.json
```

### 结构

```json
{
  "type": "ush",
  "tag": "uot-in",

  "listen": "127.0.0.1",
  "listen_port": 10086
}
```

### 监听字段

参阅 [监听字段](/configuration/shared/listen/)。**省略 `listen` 时绑 `127.0.0.1`**，这个默认值就是本协议该有的部署形态，别改成 `0.0.0.0`。

### 字段

没有协议自有字段：本入站只接受监听字段。

### 安全边界

- **本入站不认证任何东西**：谁能连上这个端口，谁就能把它当开放 UDP 中继用（目标地址由客户端指定）。唯一的防线是它只绑在回环上、外面套一层 sshd。启动时日志会明确打一行 `unauthenticated UoT listener, only reachable through the SSH tunnel in front of it`
- **sshd 侧可以收紧转发面**：给这个账号单独配 `AllowTcpForwarding yes`，再用 `PermitOpen` 决定它能转发到哪。注意 `PermitOpen` 对 **TCP 与 UoT 一视同仁**（两者都是 `direct-tcpip` 通道）：
  - `PermitOpen any`：ush 的 TCP 与 UDP 都能用，安全性完全依赖「这个账号可信」
  - `PermitOpen 127.0.0.1:10086`：只放行到 UoT 监听器 —— 实测 UDP 正常、**TCP 会被 sshd 拒掉**（客户端日志：`ssh: rejected: administratively prohibited (open failed)`），等于这个账号只做 UDP 出口
  - 想同时收窄又保留 TCP，就把允许的目标逐个列上（`PermitOpen` 支持多个条目）

  账号本身用 `PasswordAuthentication`（见下）或公钥都行
- **可以给这个账号单独开密码登录**：全局关掉密码认证、只对专用账号放开的写法是在 `/etc/ssh/sshd_config` **末尾**加

  ```
  PasswordAuthentication no
  KbdInteractiveAuthentication no

  Match User ush
      PasswordAuthentication yes
  ```

  `Match` 块必须写在文件最后（且 `Include` 之后），块内只允许 [sshd_config(5)](https://man.openbsd.org/sshd_config) 列出的那批关键字。改完用 `sshd -t` 验证语法、用
  `sshd -T -C user=ush,host=localhost,addr=127.0.0.1 | grep -i passwordauth` 看**该用户实际生效**的值（不带 `-C` 看到的只是全局默认值）。
- **UDP 的目标地址由客户端决定**：服务端不校验，所以本入站的路由规则要按「SSH 账号可信」的前提写（例如只给它 `direct`，或者用 `route.rules` 限定可用目标）

### 已知边界

- **没有用户概念**：认证完全交给 sshd 的账号体系；`ush` 侧看不到是谁在用，也无法按用户计费/限速
- **UDP 只能在 v2 UoT 上跑**：服务端是本入站时才是 v2；想接原生 OpenSSH sshd 只能用它的 TCP 转发，UDP 需要另配（如 `socks` 出站的 `udp_over_tcp` + `detour`）
- **一条 SSH 连接上的所有流共享一个 TCP 通道**：SSH 的连接级流控（窗口）会成为总带宽上限，高带宽场景要评估
