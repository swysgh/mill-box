---
icon: material/lightning-bolt
---

# Hysteria 2

Hysteria 2 是一种基于 QUIC 的简单的国产协议。
其卖点是 Brutal——一种拥塞控制算法，
它试图在丢包的情况下仍达到用户指定的带宽。

!!! warning

    尽管 GFW 很少封锁基于 UDP 的代理，但这类协议的特征实际上比基于 TCP 的代理明显得多。

| Specification                                                             | 抗被动检测 | 抗主动探测 |
|---------------------------------------------------------------------------|---------------------------|-----------------------|
| [hysteria.network](https://v2.hysteria.network/docs/developers/Protocol/) | :material-alert:          | :material-check:      |

## :material-text-box-check: 密码生成器

| 生成密码                   | 操作                                                            |
|----------------------------|-----------------------------------------------------------------|
| <code id="password"><code> | <button class="md-button" onclick="generate()">刷新</button> |

<script>
    function generate() {
        const array = new Uint8Array(16);
        window.crypto.getRandomValues(array);
        document.getElementById("password").textContent = btoa(String.fromCharCode.apply(null, array));
    }
    generate();
</script>

## :material-alert: 与官方 Hysteria 的区别

官方程序支持一种名为 **userpass** 的验证方式，
本质上是将用户名与密码的组合 `<username>:<password>` 作为实际上的密码，
而 sing-box 不提供此别名。
要将 sing-box 与官方程序一起使用，你需要填写该组合作为实际密码。

## :material-server: 服务端示例

!!! info ""

    将 `up_mbps` 和 `down_mbps` 的值替换为你服务器的实际带宽。

=== ":material-harddisk: 使用本地证书"

    ```json
     {
      "inbounds": [
        {
          "type": "hysteria2",
          "listen": "::",
          "listen_port": 8080,
          "up_mbps": 100,
          "down_mbps": 100,
          "users": [
            {
              "name": "sekai",
              "password": "<password>"
            }
          ],
          "tls": {
            "enabled": true,
            "server_name": "example.org",
            "key_path": "/path/to/key.pem",
            "certificate_path": "/path/to/certificate.pem"
          }
        }
      ]
    }
    ```

=== ":material-auto-fix: 使用 ACME"

    ```json
     {
      "inbounds": [
        {
          "type": "hysteria2",
          "listen": "::",
          "listen_port": 8080,
          "up_mbps": 100,
          "down_mbps": 100,
          "users": [
            {
              "name": "sekai",
              "password": "<password>"
            }
          ],
          "tls": {
            "enabled": true,
            "server_name": "example.org",
            "acme": {
              "domain": "example.org",
              "email": "admin@example.org"
            }
          }
        }
      ]
    }
    ```

=== ":material-cloud: 使用 ACME 和 Cloudflare API"

    ```json
     {
      "inbounds": [
        {
          "type": "hysteria2",
          "listen": "::",
          "listen_port": 8080,
          "up_mbps": 100,
          "down_mbps": 100,
          "users": [
            {
              "name": "sekai",
              "password": "<password>"
            }
          ],
          "tls": {
            "enabled": true,
            "server_name": "example.org",
            "acme": {
              "domain": "example.org",
              "email": "admin@example.org",
              "dns01_challenge": {
                "provider": "cloudflare",
                "api_token": "my_token"
              }
            }
          }
        }
      ]
    }
    ```

## :material-cellphone-link: 客户端示例

!!! info ""

    将 `up_mbps` 和 `down_mbps` 的值替换为你客户端的实际带宽。

=== ":material-web-check: 使用有效证书"

    ```json
    {
      "outbounds": [
        {
          "type": "hysteria2",
          "server": "127.0.0.1",
          "server_port": 8080,
          "up_mbps": 100,
          "down_mbps": 100,
          "password": "<password>",
          "tls": {
            "enabled": true,
            "server_name": "example.org"
          }
        }
      ]
    }
    ```

=== ":material-check: 使用自签证书"

    !!! info "提示"
        
        使用 `sing-box merge` 命令将配置与证书合并到一个文件中。

    ```json
    {
      "outbounds": [
        {
          "type": "hysteria2",
          "server": "127.0.0.1",
          "server_port": 8080,
          "up_mbps": 100,
          "down_mbps": 100,
          "password": "<password>",
          "tls": {
            "enabled": true,
            "server_name": "example.org",
            "certificate_path": "/path/to/certificate.pem"
          }
        }
      ]
    }
    ```

=== ":material-alert: 忽略证书验证"

    ```json
    {
      "outbounds": [
        {
          "type": "hysteria2",
          "server": "127.0.0.1",
          "server_port": 8080,
          "up_mbps": 100,
          "down_mbps": 100,
          "password": "<password>",
          "tls": {
            "enabled": true,
            "server_name": "example.org",
            "insecure": true
          }
        }
      ]
    }
    ```
