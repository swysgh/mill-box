---
icon: material/new-box
---

!!! question "mill-box 专有协议（上游 sing-box 没有）"

`anoiss` 用 Noise 握手取代 TLS，会话层直接复用 AnyTLS——多路复用、padding scheme、UoT、空闲会话管理都继承自 AnyTLS，因此**不需要证书，也不需要域名**。

- 握手：Noise `XX`（配置 `pre_shared_key` 后自动变成 `XXpsk3`），套件 `25519 / ChaChaPoly / BLAKE2s`，prologue 为 `anoiss/1`
- 帧：2 字节大端长度前缀 + 密文，比 TLS record 少 3 字节，所以 AnyTLS 的默认 padding scheme 可以直接沿用，不需要重新标定
- 认证分三层：客户端用 `server_public_key` 认证服务端、服务端用 `client_public_keys` 白名单认证客户端、AnyTLS 会话层再用 `users` 密码认证用户
- 支持 TCP 与 UDP（UoT），支持多路复用

### 快速上手

服务端和客户端各生成一对密钥（共两对）：

```bash
mill-box generate anoiss-keypair
# PrivateKey: <base64>
# PublicKey:  <base64>
```

服务端配置：填服务端私钥，并把**客户端公钥**放进白名单。

```json
{
  "inbounds": [
    {
      "type": "anoiss",
      "tag": "anoiss-in",
      "listen": "::",
      "listen_port": 8443,
      "server_private_key": "<服务端私钥>",
      "client_public_keys": ["<客户端公钥>"],
      "users": [
        {
          "name": "sekai",
          "password": "8JCsPssfgS8tiRwiMlhARg=="
        }
      ]
    }
  ]
}
```

客户端配置：填客户端私钥，并把**服务端公钥**填进 `server_public_key`。

```json
{
  "outbounds": [
    {
      "type": "anoiss",
      "tag": "anoiss-out",
      "server": "example.com",
      "server_port": 8443,
      "client_private_key": "<客户端私钥>",
      "server_public_key": "<服务端公钥>",
      "password": "8JCsPssfgS8tiRwiMlhARg=="
    }
  ]
}
```

写完先自检，再启动：

```bash
mill-box check -c config.json
mill-box run -c config.json
```

启动时服务端会打印服务端公钥指纹、是否启用 PSK、白名单数量，客户端会打印自身公钥指纹——用来核对两边密钥有没有配对。客户端日志里的 `client_pubkey_fingerprint` 应该等于服务端配置里 `client_public_keys` 那一项的指纹。

### 结构

```json
{
  "type": "anoiss",
  "tag": "anoiss-in",

  "listen": "::",
  "listen_port": 8443,

  "server_private_key": "",
  "client_public_keys": [],
  "allow_any_client": false,
  "users": [
    {
      "name": "sekai",
      "password": "8JCsPssfgS8tiRwiMlhARg=="
    }
  ],
  "padding_scheme": [],
  "pre_shared_key": "",
  "handshake_timeout": "5s",
  "handshake_padding": 512,
  "session_auth_timeout": "10s"
}
```

### 监听字段

参阅 [监听字段](/configuration/shared/listen/)。

### 字段

#### server_private_key

==必填==

服务端 Noise 静态私钥，base64 编码的 32 字节（RawURL 或标准 base64 都接受）。对应的公钥由私钥推导，配置里不用写。生成方式见[快速上手](#快速上手)。

#### client_public_keys

==必填==（与 `allow_any_client` 二选一，都不填会启动失败）

允许连接的客户端 Noise 静态公钥白名单（base64 编码的 32 字节）。Noise 握手完成后比对对端静态公钥，不在白名单里的连接会被直接关闭，并记录一条带对端公钥的 warn 日志。

#### allow_any_client

接受任意客户端公钥。**仅供测试**，开启时启动打 warn。开启后服务端不再区分客户端身份，安全性只剩 `users` 密码与 `pre_shared_key`。

#### users

==必填==

AnyTLS 会话层用户列表，语义与 [AnyTLS](/configuration/inbound/anytls/) 相同：会话首部携带密码，服务端用 SHA256 比对。列表为空时所有会话都会认证失败。

#### padding_scheme

AnyTLS padding scheme 行数组，格式与语义见 [AnyTLS](/configuration/inbound/anytls/)。两点注意：

- 服务端配置的 scheme 在会话建立后通过 `commandUpdatePaddingScheme` 下发给客户端，因此**从第二个会话起才生效**，客户端第一个会话用的是库内置的默认 scheme
- `index 0` 对应认证请求的填充，长度字段是 uint16，所以 `index 0` 的 max 不能超过 `65535`（配置校验会拦下）

#### pre_shared_key

可选的预共享密钥（base64 编码的 32 字节）。配置后 Noise 握手变成 `XXpsk3`：即使静态私钥泄露，没有 PSK 也完不成握手。**两端必须填同一个值**。

#### handshake_timeout

Noise 握手超时。默认 5 秒。（未配置时启动日志会打印 `handshake_timeout=0s`，那是配置里的原始值，实际生效的是 5 秒。）

#### handshake_padding

三条握手消息的目标总长度（字节，含 2 字节长度前缀），用来消除握手的长度指纹。默认 `512`；`0` 表示不填充；其他取值必须在 `128`–`65535` 之间。取值太小（装不下最小的那条握手消息）会在启动时报错。

#### session_auth_timeout

握手完成到 AnyTLS 认证完成之间允许的读超时。默认 10 秒。认证数据（32 字节哈希 + 2 字节填充长度 + 填充）全部交付后立刻清除该 deadline，之后的长连接不受影响；只发 1 个字节就挂住的连接会被关闭。
