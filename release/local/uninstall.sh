#!/usr/bin/env bash

set -e -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/common.sh"

echo "Uninstalling mill-box..."

if systemctl is-active --quiet mill-box 2>/dev/null; then
    echo "Stopping mill-box service..."
    sudo systemctl stop mill-box
fi

if systemctl is-enabled --quiet mill-box 2>/dev/null; then
    echo "Disabling mill-box service..."
    sudo systemctl disable mill-box
fi

echo "Removing files..."
sudo rm -rf "$INSTALL_DATA_PATH"
sudo rm -rf "$INSTALL_BIN_PATH/$BINARY_NAME"
sudo rm -rf "$INSTALL_CONFIG_PATH"
sudo rm -rf "$SYSTEMD_SERVICE_PATH/mill-box.service"

echo "Reloading systemd..."
sudo systemctl daemon-reload

echo ""
echo "Uninstallation complete!"
