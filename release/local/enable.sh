#!/usr/bin/env bash

set -e -o pipefail

sudo systemctl enable mill-box
sudo systemctl start mill-box
sudo journalctl -u mill-box --output cat -f
