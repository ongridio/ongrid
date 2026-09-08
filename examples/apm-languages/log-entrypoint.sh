#!/bin/sh
set -eu
# Dedicated host files are ingested by the existing Edge logs plugin.
exec "$@" >> "/logs/${APP_LANGUAGE}.log" 2>&1
