#!/usr/bin/env bash
# 用 dpkg-deb 原生打 mill-box 的 deb 包（不依赖 fpm/gem/gpg）。
# 文件布局照 .fpm_systemd（去掉仓库里已删除的 LICENSE）。
# 用法：
#   ./release/deb/build_deb.sh                 # 本机 arch，默认 tags，装进 dist/
#   ./release/deb/build_deb.sh --arch arm64     # 交叉编译 ARM64（手机/ARM 机）
#   ./release/deb/build_deb.sh --arch amd64 --tags "with_clash_api,with_wireguard"
# 产物：dist/mill-box_<version>_<arch>.deb
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DIST_DIR="$PROJECT_DIR/dist"
BINARY=mill-box

ARCH="$(go env GOARCH)"
VERSION=""
EXTRA_TAGS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --arch) ARCH="$2"; shift 2 ;;
        --version) VERSION="$2"; shift 2 ;;
        --tags) EXTRA_TAGS="$2"; shift 2 ;;
        -h|--help) grep '^#' "$0" | sed 's/^# \{0,2\}//'; exit 0 ;;
        *) echo "未知参数: $1" >&2; exit 1 ;;
    esac
done

setup_env() {
    [ -d /usr/local/go ] && export PATH="$PATH:/usr/local/go/bin"
    command -v go >/dev/null || { echo "找不到 go，先装好 Go" >&2; exit 1; }
    local ver
    ver="$(go version 2>/dev/null | awk '{print $3}' | sed 's/go//')"
    echo "使用 go $ver" >&2
}

get_version() {
    cd "$PROJECT_DIR"
    export GOTOOLCHAIN=local
    if [ -n "$VERSION" ]; then
        echo "$VERSION"; return
    fi
    # 从 git tag 读；读不到就退成一个可装的固定号
    local v
    v="$(CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go run ./cmd/internal/read_tag 2>/dev/null || true)"
    case "$v" in ""|unknown) v="1.14.2-mill" ;; esac
    # deb 版本字段不能含 git describe 里的 -g<sha>，取第一个 tag 段
    echo "${v%%-*}"
}

get_tags() {
    local base
    base="$(cat "$PROJECT_DIR/release/DEFAULT_BUILD_TAGS_OTHERS")"
    if [ -n "$EXTRA_TAGS" ]; then
        echo "$base,$EXTRA_TAGS"
    else
        echo "$base"
    fi
}

build_binary() {
    local tags="$1" ldflags out
    local shared
    shared="$(cat "$PROJECT_DIR/release/LDFLAGS")"
    ldflags="-X 'github.com/swysgh/mill-box/constant.Version=${VERSION}' ${shared} -s -w -buildid="
    out="$DIST_DIR/${BINARY}-linux-${ARCH}"
    echo "编译 $out (arch=$ARCH tags=$tags)"
    cd "$PROJECT_DIR"
    export GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH"
    go build -trimpath -ldflags "$ldflags" -tags "$tags" -o "$out" ./cmd/mill-box
    echo "  产物: $(stat -c%s "$out") 字节, $( "$out" version 2>/dev/null | head -1 )"
}

assemble() {
    local stage="$DIST_DIR/.deb-stage"
    rm -rf "$stage"; mkdir -p "$stage"
    local pkg="$stage/DEBIAN"
    local usr="$stage/usr" etc="$stage/etc"
    mkdir -p "$usr/bin" "$usr/lib/systemd/system" \
        "$usr/lib/sysusers.d" "$usr/share/polkit-1/rules.d" \
        "$usr/share/dbus-1/system.d" \
        "$usr/share/bash-completion/completions" \
        "$usr/share/fish/vendor_completions.d" "$usr/share/zsh/site-functions" \
        "$pkg"

    # 二进制
    install -m 755 "$DIST_DIR/${BINARY}-linux-${ARCH}" "$usr/bin/$BINARY"

    # 配置（/etc，conffile，升级不覆盖用户改动）
    install -D -m 644 "$PROJECT_DIR/release/config/config.json" "$etc/$BINARY/config.json"

    # systemd 单元
    install -m 644 "$PROJECT_DIR/release/config/mill-box.service"    "$usr/lib/systemd/system/$BINARY.service"
    install -m 644 "$PROJECT_DIR/release/config/mill-box@.service"  "$usr/lib/systemd/system/$BINARY@.service"

    # sysusers（建系统用户）
    install -m 644 "$PROJECT_DIR/release/config/mill-box.sysusers" "$usr/lib/sysusers.d/$BINARY.conf"

    # polkit / dbus（tun/split-dns 走 dbus 权限）
    install -m 644 "$PROJECT_DIR/release/config/mill-box.rules"      "$usr/share/polkit-1/rules.d/$BINARY.rules"
    install -m 644 "$PROJECT_DIR/release/config/mill-box-split-dns.xml" "$usr/share/dbus-1/system.d/$BINARY-split-dns.conf"

    # 补全
    install -m 644 "$PROJECT_DIR/release/completions/mill-box.bash" "$usr/share/bash-completion/completions/$BINARY.bash"
    install -m 644 "$PROJECT_DIR/release/completions/mill-box.fish" "$usr/share/fish/vendor_completions.d/$BINARY.fish"
    install -m 644 "$PROJECT_DIR/release/completions/mill-box.zsh"  "$usr/share/zsh/site-functions/_$BINARY"

    # control：手写（不依赖 fpm）。Depends 留空，需要 glibc 就填 libc6
    cat > "$pkg/control" <<EOF
Package: $BINARY
Version: $VERSION
Architecture: $ARCH
Maintainer: swysgh
Description: The universal proxy platform (mill-box, a sing-box fork)
Section: net
Priority: optional
EOF

    # postinst：建用户 + daemon-reload。修掉上游的 bug——systemd-sysusers 传的是
    # 装好的绝对路径（dpkg 跑脚本时 cwd=/，上游写的相对路径 mill-box.conf 找不到）
    cat > "$pkg/postinst" <<EOF
#!/bin/sh
set -e
if command -v systemd-sysusers >/dev/null 2>&1; then
    systemd-sysusers /usr/lib/sysusers.d/$BINARY.conf || true
fi
if [ -d /run/systemd/system ]; then
    systemctl --system daemon-reload >/dev/null 2>&1 || true
fi
EOF
    chmod 755 "$pkg/postinst"

    # prerm：卸载前停服务
    cat > "$pkg/prerm" <<EOF
#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
    systemctl stop $BINARY.service >/dev/null 2>&1 || true
    systemctl stop "$BINARY@.service" >/dev/null 2>&1 || true
fi
EOF
    chmod 755 "$pkg/prerm"

    # conffile：/etc 下的文件 dpkg 默认就当 conffile 处理（升级保留用户改动），
    # 无需单独的 conffiles 文件；control 里也不额外声明
}

package() {
    local out="$DIST_DIR/${BINARY}_${VERSION}_${ARCH}.deb"
    dpkg-deb --build --root-owner-group "$DIST_DIR/.deb-stage" "$out"
    rm -rf "$DIST_DIR/.deb-stage"
    echo "打包完成: $out ($(stat -c%s "$out") 字节)"
    dpkg-deb -c "$out" | sed 's/^/  /'
}

setup_env
VERSION="$(get_version)"
build_binary "$(get_tags)"
assemble
package
echo "done: $DIST_DIR/${BINARY}_${VERSION}_${ARCH}.deb"
