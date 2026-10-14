---
icon: material/new-box
---

!!! question "mill-box 专有协议（上游 sing-box 没有）"

出站侧与 [入站](/configuration/inbound/anoiss/) 对称：Noise 握手取代 TLS，会话层复用 AnyTLS（多路复用、padding、UoT、空闲会话管理），不需要证书和域名。完整的服务端 + 客户端示例见[入站的快速上手](/configuration/inbound/anoiss/#快速上手)。

### 结构

```json
{
  "type": "anoiss",
  "tag": "anoiss-out",

  "server": "127.0.0.1",
  "server_port": 8443,

  "client_private_key": "",
  "server_public_key": "",
  "allow_any_server": false,
  "password": "8JCsPssfgS8tiRwiMlhARg==",
  "pre_shared_key": "",
  "handshake_timeout": "5s",
  "handshake_padding": 512,
  "idle_session_check_interval": "30s",
  "idle_session_timeout": "30s",
  "min_idle_session": 5,
  "client_metadata": ""
}
```

### 字段

#### server

==必填==

服务器地址。

#### server_port

==必填==

服务器端口。

#### client_private_key

==必填==

客户端 Noise 静态私钥，base64 编码的 32 字节（RawURL 或标准 base64 都接受）。用 `mill-box generate anoiss-keypair` 生成，对应的公钥要填进服务端的 `client_public_keys`。

#### server_public_key

==必填==（除非设置 `allow_any_server`）

服务端 Noise 静态公钥（base64 编码的 32 字节）。握手完成后比对，不一致立即断开——这是客户端识别服务端的唯一依据，**不要为了省事关掉**。

#### allow_any_server

关闭服务端认证，接受任意服务端。**不安全**：中间人可以冒充服务端完成握手并读取会话首部的 AnyTLS 密码摘要，反过来冒充你连接真服务端。开启时启动打 warn。同时配置 `server_public_key` 时，`allow_any_server` 优先（即不做 pin）。

#### password

==必填==

AnyTLS 会话密码，对应服务端 `users` 里的 `password`。

#### pre_shared_key

可选的预共享密钥（base64 编码的 32 字节），Noise 握手变为 `XXpsk3`。**必须与服务端一致**。

#### handshake_timeout

Noise 握手超时。默认 5 秒。

#### handshake_padding

握手消息的目标总长度（字节），默认 `512`；`0` 表示不填充；其他取值必须在 `128`–`65535` 之间。两端不必配成同一个值。

#### idle_session_check_interval

检查空闲会话的时间间隔。默认值：30秒。

#### idle_session_timeout

在检查中，关闭闲置时间超过此值的会话。默认值：30秒。

#### min_idle_session

在检查中，至少前 `n` 个空闲会话保持打开状态。默认值：`n`=0

#### client_metadata

参阅 [AnyTLS 客户端元数据](/manual/misc/anytls-client-metadata/)。

### 拨号字段

参阅 [拨号字段](/configuration/shared/dial/)。
