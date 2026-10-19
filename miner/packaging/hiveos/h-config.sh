#!/usr/bin/env bash
# Writes Ember's one config file from the flight sheet: CUSTOM_URL = pool host:port (a second one after a comma is
# the backup), CUSTOM_TEMPLATE = the wallet, CUSTOM_PASS unused, CUSTOM_USER_CONFIG = extra "key = value" lines
# (tariff, currency, no_tls, no_pool_fee, algo).
[ -t 1 ] && . colors
[ -z "$CUSTOM_TEMPLATE" ] && echo -e "${YELLOW}CUSTOM_TEMPLATE (the wallet) is empty${NOCOLOR}" && return
[ -z "$CUSTOM_URL" ] && echo -e "${YELLOW}CUSTOM_URL (the pool) is empty${NOCOLOR}" && return
pool=${CUSTOM_URL%%,*}; backup=""; case "$CUSTOM_URL" in *,*) backup=${CUSTOM_URL#*,};; esac
pool=${pool#stratum+tcp://}; pool=${pool#stratum+ssl://}; backup=${backup#stratum+tcp://}
{
  echo "pool = $pool"
  [ -n "$backup" ] && echo "pool_backup = $backup"
  echo "address = $CUSTOM_TEMPLATE"
  echo "worker_name = ${WORKER_NAME:-hive}"
  echo "algo = igneumhash"
  echo "no_tls = true"
  echo "api = 127.0.0.1:4068"
  echo "workers_dir = /hive/miners/custom/ember/workers"
  echo "data_dir = /hive/miners/custom/ember/state"
  [ -n "$CUSTOM_USER_CONFIG" ] && echo "$CUSTOM_USER_CONFIG" | tr ';' '\n'
} > "$CUSTOM_CONFIG_FILENAME"
