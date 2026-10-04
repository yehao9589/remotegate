#!/bin/sh
set -eu
# A fresh bind-mounted data directory must be writable by the service account.
mkdir -p /data
chown -R remotegate:remotegate /data
chmod 700 /data
if [ -z "${INSTALLER_PATH:-}" ]; then
    for package_path in /opt/remotegate/packages/RemoteGate-*-istore.run; do
        if [ -f "$package_path" ]; then
            export INSTALLER_PATH="$package_path"
            break
        fi
    done
fi
exec su-exec remotegate "$@"
