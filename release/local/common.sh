#!/usr/bin/env bash

set -e -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
BINARY_NAME="mill-box"

INSTALL_BIN_PATH="/usr/local/bin"
INSTALL_CONFIG_PATH="/usr/local/etc/mill-box"
INSTALL_DATA_PATH="/var/lib/mill-box"
SYSTEMD_SERVICE_PATH="/etc/systemd/system"

DEFAULT_BUILD_TAGS="$(cat "$PROJECT_DIR/release/DEFAULT_BUILD_TAGS_OTHERS")"

setup_environment() {
    if [ -d /usr/local/go ]; then
        export PATH="$PATH:/usr/local/go/bin"
    fi

    if ! command -v go &> /dev/null; then
        echo "Error: Go is not installed or not in PATH"
        echo "Run install_go.sh to install Go"
        exit 1
    fi
}

get_build_tags() {
    local extra_tags="$1"
    if [ -n "$extra_tags" ]; then
        echo "${DEFAULT_BUILD_TAGS},${extra_tags}"
    else
        echo "${DEFAULT_BUILD_TAGS}"
    fi
}

get_version() {
    cd "$PROJECT_DIR"
    GOHOSTOS=$(go env GOHOSTOS)
    GOHOSTARCH=$(go env GOHOSTARCH)
    CGO_ENABLED=0 GOOS=$GOHOSTOS GOARCH=$GOHOSTARCH go run ./cmd/internal/read_tag
}

get_ldflags() {
    local version
    version=$(get_version)
    local shared_ldflags
    shared_ldflags=$(cat "$PROJECT_DIR/release/LDFLAGS")
    echo "-X 'github.com/swysgh/mill-box/constant.Version=${version}' ${shared_ldflags} -s -w -buildid="
}

build_mill_box() {
    local tags="$1"
    local ldflags
    ldflags=$(get_ldflags)

    echo "Building mill-box with tags: $tags"
    cd "$PROJECT_DIR"
    export GOTOOLCHAIN=local
    go install -v -trimpath -ldflags "$ldflags" -tags "$tags" ./cmd/mill-box
}

install_binary() {
    local gopath
    gopath=$(go env GOPATH)
    echo "Installing binary to $INSTALL_BIN_PATH/$BINARY_NAME"
    sudo cp "${gopath}/bin/${BINARY_NAME}" "${INSTALL_BIN_PATH}/"
}

setup_config() {
    echo "Setting up configuration"
    sudo mkdir -p "$INSTALL_CONFIG_PATH"
    if [ ! -f "$INSTALL_CONFIG_PATH/config.json" ]; then
        sudo cp "$PROJECT_DIR/release/config/config.json" "$INSTALL_CONFIG_PATH/config.json"
        echo "Default config installed to $INSTALL_CONFIG_PATH/config.json"
    else
        echo "Config already exists at $INSTALL_CONFIG_PATH/config.json (not overwriting)"
    fi
}

setup_systemd() {
    echo "Setting up systemd service"
    sudo cp "$SCRIPT_DIR/mill-box.service" "$SYSTEMD_SERVICE_PATH/"
    sudo systemctl daemon-reload
}

stop_service() {
    if systemctl is-active --quiet mill-box; then
        echo "Stopping mill-box service"
        sudo systemctl stop mill-box
    fi
}

start_service() {
    echo "Starting mill-box service"
    sudo systemctl start mill-box
}

restart_service() {
    echo "Restarting mill-box service"
    sudo systemctl restart mill-box
}
