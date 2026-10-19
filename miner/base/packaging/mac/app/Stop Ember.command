#!/bin/bash
# Stops a running Ember: asks the engine to quit through its local API (miner first, then the node), waits,
# then ends whatever is left from the app bundle. an earlier release, 4 October 2026.
URL_FILE="$HOME/Library/Application Support/Igneum/app/app.url"
if [ -f "$URL_FILE" ]; then
    url="$(cat "$URL_FILE")"
    if curl -s -m 5 -X POST "${url}api/quit" -d '{}' >/dev/null 2>&1; then
        echo "asked Ember to stop (miner first, then the node)"
        for i in $(seq 1 50); do pgrep -f 'Ember.app/Contents/' >/dev/null || break; sleep 1; done
    fi
fi
left="$(pgrep -f 'Ember.app/Contents/' 2>/dev/null)"
if [ -n "$left" ]; then
    echo "ending leftover processes: $(echo $left | tr '\n' ' ')"
    pkill -TERM -f 'Ember.app/Contents/' 2>/dev/null; sleep 5
    pkill -KILL -f 'Ember.app/Contents/' 2>/dev/null
fi
echo "Ember is stopped."
