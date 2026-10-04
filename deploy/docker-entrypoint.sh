#!/bin/sh
set -eu
# A fresh bind-mounted data directory must be writable by the service account.
mkdir -p /data
chown -R remotegate:remotegate /data
chmod 700 /data
exec su-exec remotegate "$@"
