#!/bin/sh
# Local dev bridge control. Runs the `sh-bitrix-dev` Beeper registration from dev/ (gitignored),
# separate from the prod deployment. Usage: ./dev.sh start|stop|restart|status|logs|build
set -eu
cd "$(dirname "$0")"
DIR=dev
CONFIG=$DIR/config.yaml
PIDFILE=$DIR/bridge.pid
LOG=$DIR/logs/stdout.log

running() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }

case "${1:-status}" in
build)
	nix shell nixpkgs#go -c ./build.sh
	;;
start)
	if running; then echo "already running (pid $(cat "$PIDFILE"))"; exit 0; fi
	[ -f "$CONFIG" ] || { echo "missing $CONFIG; generate with: (cd $DIR && bbctl config --type bridgev2 -o config.yaml sh-bitrix-dev)"; exit 1; }
	[ -x ./matrix-bitrix ] || "$0" build
	mkdir -p "$DIR/logs"
	cd "$DIR"
	nohup ../matrix-bitrix -c config.yaml >>logs/stdout.log 2>&1 &
	echo $! >bridge.pid
	cd ..
	echo "started (pid $(cat "$PIDFILE")), logs: $LOG"
	;;
stop)
	if ! running; then echo "not running"; rm -f "$PIDFILE"; exit 0; fi
	kill -INT "$(cat "$PIDFILE")"
	for _ in 1 2 3 4 5 6 7 8 9 10; do running || break; sleep 1; done
	running && kill -KILL "$(cat "$PIDFILE")"
	rm -f "$PIDFILE"
	echo "stopped"
	;;
restart)
	"$0" stop
	"$0" start
	;;
status)
	if running; then echo "running (pid $(cat "$PIDFILE"))"; else echo "stopped"; fi
	;;
logs)
	tail -f "$LOG"
	;;
*)
	echo "usage: $0 start|stop|restart|status|logs|build" >&2
	exit 2
	;;
esac
