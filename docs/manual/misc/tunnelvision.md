---
icon: material/book-lock-open
---

# TunnelVision

TunnelVision 是一种攻击，它利用 DHCP 选项 121 设置更高优先级的路由，
从而使流量不经过 VPN。

参考：https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2024-3661

## 状态

### Android

Android 不处理 DHCP 选项 121，因此不受影响。

### Apple 平台

将 [sing-box 图形客户端](/clients/apple/#download) 更新到 `1.9.0-rc.16` 或更高版本，
然后在 `Settings` — `Packet Tunnel` 中启用 `includeAllNetworks`，你就不会受到影响。

自 sing-box 1.15.0 起，sing-tun 自有的 TCP/IP 栈支持 `includeAllNetworks`。
移除 `stack` 选项即可使用它。旧的 `system` 和 `mixed` 栈在启用
`includeAllNetworks` 时不可用。

### Linux

将 sing-box 更新到 `1.9.0-rc.16` 或更高版本，由 `auto-route` 生成的路由不受影响。

### Windows

尚无解决方案。

## 变通方法

* 不要连接到不受信任的网络
* 通过另一台设备中继不受信任的网络
* 直接忽略它
