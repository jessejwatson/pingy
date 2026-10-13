# Streams one host's pings back to pingy over ssh; see remote.go for what
# it sends. It's all one function so the shell reads it whole before any
# of it runs, and nothing it runs reads the rest of it from stdin.
main() {
	exec </dev/null
	target=$1 iv=$2 since=$3

	# A non-login shell's PATH often misses where pingy is installed.
	p=$(command -v pingy 2>/dev/null)
	for c in "$HOME/.local/bin/pingy" /opt/homebrew/bin/pingy /usr/local/bin/pingy "$HOME/go/bin/pingy"; do
		[ -z "$p" ] && [ -x "$c" ] && p=$c
	done
	# pingy --origin asks what's here: "pingy <version>", "old <version>"
	# for a pingy that can't stream, or "none".
	if [ "$target" = --probe ]; then
		if [ -z "$p" ]; then
			echo none
		elif "$p" __stream --check >/dev/null 2>&1; then
			echo "pingy $("$p" --version 2>/dev/null | awk '{ print $2 }')"
		else
			echo "old $("$p" --version 2>/dev/null | awk '{ print $2 }')"
		fi
		return
	fi
	if [ -n "$p" ] && "$p" __stream --check >/dev/null 2>&1; then
		exec "$p" __stream "$target" "$iv" "$since"
	fi

	echo "pingy-stream 1 script"
	wait_ms=$iv
	[ "$wait_ms" -gt 2000 ] && wait_ms=2000
	case $(uname) in
	Darwin | *BSD) w="-W $wait_ms" ;; # BSD ping's -W is in milliseconds
	*) w="-W $(((wait_ms + 999) / 1000))" ;;
	esac
	n=-n
	case $(ping 2>&1) in *BusyBox*) n= ;; esac
	secs=$(awk "BEGIN { print $iv / 1000 }")

	addr=?
	while :; do
		# The interval is timed by a sleep alongside each ping, so a slow
		# reply doesn't push the next ping back.
		sleep "$secs" &
		out=$(ping $n -c 1 $w "$target" 2>&1)
		a=$(printf '%s\n' "$out" | sed -n 's/^PING[^(]*(\([^)]*\)).*/\1/p' | head -n 1)
		if [ "$a" != "$addr" ]; then
			if [ -n "$a" ]; then echo "= $a"; else echo "! can't resolve $target"; fi
			addr=$a
		fi
		# echo is the shell's own, so once ssh has gone, the shell is
		# what's stopped by the broken pipe.
		ms=$(printf '%s\n' "$out" | sed -n 's/.*time[=<] *\([0-9.]*\) *ms.*/\1/p' | head -n 1)
		if [ -n "$ms" ]; then
			echo "- $(awk "BEGIN { printf \"%d\", $ms * 1000 }")"
		else
			echo "- -1"
		fi
		wait
	done
}
main "$@"
