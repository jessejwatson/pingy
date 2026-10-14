# Streams one host's pings back to pingy over ssh; see remote.go for what
# it sends. It's all functions, called on the last line, so the shell
# reads it whole before any of it runs, and nothing it runs reads the rest
# of it from stdin.
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

	# Anywhere else this script pings by itself. The pinging is done in
	# the background into a cache in /tmp, and this shell only passes on
	# what's new there, so a connection that drops doesn't stop the pings:
	# connecting again within a while (key names the host being pinged,
	# and from says what of the cache pingy already has) carries on with
	# the same pinger and sends what was missed. With no one connected for
	# hold ms, the pinger deletes the cache and stops.
	key=$4 from=${5:--}
	hold=10000
	keep=$((120000 / iv + 10)) # pings kept in the cache, about two minutes
	wait_ms=$iv
	[ "$wait_ms" -gt 2000 ] && wait_ms=2000
	case $(uname) in
	Darwin | *BSD) w="-W $wait_ms" ;; # BSD ping's -W is in milliseconds
	*) w="-W $(((wait_ms + 999) / 1000))" ;;
	esac
	n=-n
	case $(ping 2>&1) in *BusyBox*) n= ;; esac
	secs=$(awk "BEGIN { print $iv / 1000 }")

	echo "pingy-stream 1 script"
	umask 077
	base=${TMPDIR:-/tmp}
	base=${base%/}/pingy-cache-$(id -u)
	mkdir -p "$base" 2>/dev/null
	# Anyone can make a directory in /tmp, so check it's this user's own.
	set -- $(ls -ldn "$base" 2>/dev/null)
	case $1 in drwx------*) ;; *) set -- ;; esac
	if [ "${3:-}" != "$(id -u)" ] || [ -z "$key" ]; then
		echo "! can't keep pings in $base"
		exit 1
	fi
	d=$base/$key

	mkdir -p "$d" && echo $$ >"$d/reader"
	run= sent=0
	while :; do
		# A newer connection has taken over: leave it to that one.
		[ "$(cat "$d/reader" 2>/dev/null)" = $$ ] || [ ! -d "$d" ] || exit 0
		p=$(cat "$d/pinger" 2>/dev/null)
		if [ -z "$p" ] || ! kill -0 "$p" 2>/dev/null; then
			mkdir -p "$d"
			echo $$ >"$d/reader"
			: >"$d/samples"
			rm -f "$d/status"
			echo "$(date +%s).$$" >"$d/run"
			pinger </dev/null >/dev/null 2>&1 &
			echo $! >"$d/pinger"
		fi
		r=$(cat "$d/run" 2>/dev/null)
		if [ "$r" != "$run" ]; then
			# A pinger this connection hasn't sent from yet: from says
			# how far pingy got with it, if it's the one pingy knows.
			sent=0
			[ -z "$run" ] && [ "${from%:*}" = "$r" ] && sent=${from##*:}
			run=$r
			echo "@ $run"
			[ -s "$d/status" ] && cat "$d/status"
		fi
		# The pings since the last sent, each with how long before the
		# newest it was, then # and the newest's number.
		out=$(awk -v s="$sent" -v iv="$iv" '
			$1 + 0 > s + 0 { n++; seq[n] = $1; rest[n] = substr($0, length($1) + 2) }
			END {
				for (i = 1; i <= n; i++) {
					if (rest[i] ~ /^[=!]/) print rest[i]
					else print "+ " seq[i] " " (seq[n] - seq[i]) * iv " " rest[i]
				}
				printf "#%s", n ? seq[n] : s
			}' "$d/samples" 2>/dev/null)
		case $out in *"#"*) sent=${out##*#} out=${out%#*} ;; esac
		# printf is the shell's own, so once ssh has gone, the shell is
		# what's stopped by the broken pipe.
		[ -n "$out" ] && { printf '%s' "$out" || exit 1; }
		sleep "$secs"
	done
}

# pinger pings target every iv ms into the cache, as lines of
# "<number> <rtt µs>" (-1 for lost), and "<number> = <address>" or
# "<number> ! <problem>" before the ping they're news for, until no one
# has been connected for hold ms.
pinger() {
	trap '' HUP PIPE
	seq=0 addr=? alone=0 lines=0
	while :; do
		# The interval is timed by a sleep alongside each ping, so a slow
		# reply doesn't push the next ping back.
		sleep "$secs" &
		out=$(ping $n -c 1 $w "$target" 2>&1)
		a=$(printf '%s\n' "$out" | sed -n 's/^PING[^(]*(\([^)]*\)).*/\1/p' | head -n 1)
		if [ "$a" != "$addr" ]; then
			if [ -n "$a" ]; then s="= $a"; else s="! can't resolve $target"; fi
			echo "$((seq + 1)) $s" >>"$d/samples"
			echo "$s" >"$d/status"
			addr=$a
		fi
		ms=$(printf '%s\n' "$out" | sed -n 's/.*time[=<] *\([0-9.]*\) *ms.*/\1/p' | head -n 1)
		rtt=-1
		[ -n "$ms" ] && rtt=$(awk "BEGIN { printf \"%d\", $ms * 1000 }")
		seq=$((seq + 1))
		echo "$seq $rtt" >>"$d/samples"
		lines=$((lines + 1))
		if [ "$lines" -ge $((keep * 2)) ]; then
			tail -n "$keep" "$d/samples" >"$d/samples.t" && mv "$d/samples.t" "$d/samples"
			lines=$keep
		fi
		r=$(cat "$d/reader" 2>/dev/null)
		if [ -n "$r" ] && kill -0 "$r" 2>/dev/null; then alone=0; else alone=$((alone + iv)); fi
		wait
		[ "$alone" -ge "$hold" ] && break
		[ -d "$d" ] || return
	done
	rm -rf "$d"
	rmdir "$base" 2>/dev/null
}
main "$@"
