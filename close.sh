#!/bin/bash


source ./vars.env

PID_FILE=$TESTNET_DIR/PIDS.pid

# First parameter is the file with
# one pid per line.
if [ -f "$PID_FILE" ]; then
  while read pid
    do
      # handle the case of blank lines
      [[ -n "$pid" ]] || continue

      echo killing $pid
      kill -9 $pid || true
    done < $PID_FILE
fi
