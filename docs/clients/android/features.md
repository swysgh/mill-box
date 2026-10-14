# :material-decagram: 功能

#### 界面选项

* 在通知中显示实时网络速度

#### 服务

SFA 允许您通过 ForegroundService 或 VpnService（当需要 TUN 时）运行 sing-box。

#### TUN

SFA 通过 Android VpnService 提供非特权 TUN 实现。

| TUN 入站字段                     | 可用             | 备注                 |
|-------------------------------|------------------|----------------------|
| `interface_name`              | :material-close: | 由 Android 管理      |
| `inet4_address`               | :material-check: | /                    |
| `inet6_address`               | :material-check: | /                    |
| `mtu`                         | :material-check: | /                    |
| `gso`                         | :material-close: | 无权限               |
| `auto_route`                  | :material-check: | /                    |
| `strict_route`                | :material-close: | 未实现               |
| `inet4_route_address`         | :material-check: | /                    |
| `inet6_route_address`         | :material-check: | /                    |
| `inet4_route_exclude_address` | :material-check: | /                    |
| `inet6_route_exclude_address` | :material-check: | /                    |
| `stack`                       | :material-check: | /                    |
| `include_interface`           | :material-close: | 无权限               |
| `exclude_interface`           | :material-close: | 无权限               |
| `include_uid`                 | :material-close: | 无权限               |
| `exclude_uid`                 | :material-close: | 无权限               |
| `include_android_user`        | :material-close: | 无权限               |
| `include_package`             | :material-check: | /                    |
| `exclude_package`             | :material-check: | /                    |
| `platform`                    | :material-check: | /                    |

| 路由/DNS 规则字段         | 可用             | 备注                 |
|-----------------------|------------------|----------------------|
| `process_name`        | :material-close: | 无权限               |
| `process_path`        | :material-close: | 无权限               |
| `process_path_regex`  | :material-close: | 无权限               |
| `package_name`        | :material-check: | /                    |
| `package_name_regex`  | :material-check: | /                    |
| `user`                | :material-close: | 改用 `package_name`  |
| `user_id`             | :material-close: | 改用 `package_name`  |
| `wifi_ssid`           | :material-check: | 需要精确位置权限     |
| `wifi_bssid`          | :material-check: | 需要精确位置权限     |

### 覆盖

使用平台特定的值覆盖配置文件配置项。

#### 按应用代理

SFA 允许您在图形界面中选择需要代理或绕过的 Android 应用列表，以覆盖 `include_package` 和
`exclude_package` 配置项。

特别地，该选择器还提供「中国应用」扫描功能，为中国用户提供出色的体验，以绕过不需要代理的应用。
具体而言，通过 dex 类路径及其他方式扫描中国应用或 SDK 的特征，几乎不会有漏报。

### 杂项

* 工作目录位于 `/sdcard/Android/data/com.swysgh.millbox/files`（外部文件目录）
* 崩溃日志位于 `$working_directory/stderr.log`
