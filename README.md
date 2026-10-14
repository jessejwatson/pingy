# pingy

Keep an eye on many pings at once. Start pinging a host in the background
with `pingy <host> -d`, as many as you like, then list them with `pingy -l`,
graph them all with `pingy -g`, or run `pingy` for a panel where you search
the hosts and see each one's recent graph. Opening a host shows its whole
history, which you can move back through, zoom, rename, pause or delete.

To watch a few hosts together, press ctrl-t in the panel, tab to pick them and
enter to graph them; press `s` there to save them as a view, which
`pingy -g <view>` opens again.

`pingy <host>` without `-d` pings a host and shows its graph straight away;
press `d` to leave it running in the background, or `q` to stop.

```sh
pingy 1.1.1.1 -d -n cloudflare   # in the background, named
pingy router.lan 8.8.8.8 -d      # several at once
pingy -l                         # list them
pingy -g                         # graph them all, live
pingy -g office                  # graph a saved view
pingy                            # the panel
pingy -k cloudflare              # stop one and delete its history
pingy -p                         # pause them all, keeping their history
pingy -r                         # start stopped ones again, e.g. after a restart
pingy --history                  # how much disk history takes
pingy --history 128M             # keep up to 128 MB per host
```

## From other machines

The same host can be pinged from other machines too, over ssh, and graphed
side by side with how it looks from here, which tells a slow host from a
slow network:

```sh
pingy --origin office jesse@office.lan   # name a machine, and check it's reachable
pingy 1.1.1.1 --from office,home -d      # ping from there
pingy 1.1.1.1@jesse@lab.lan -d           # or from anywhere ssh reaches
pingy -g 1.1.1.1                         # graph it from everywhere at once
```

When ssh can't get in by itself (a machine it hasn't seen before, or one
that wants a password) pingy shows ssh's prompts in a popup, or right in
the terminal for `pingy --origin` and `-d`, and keeps that login open for
every host pinged from there until the last one is stopped. A key
(`ssh-copy-id`) saves logging in again after the connection drops. If
pingy is installed there, it pings the host in the background as
`pingy <host> -d` would, so it carries on while you're away, and the gap is
filled in when you're back; `pingy -k` here leaves it running there. If it
isn't, a small shell script pings the host instead, for as long as the
connection lasts, so nothing has to be installed.

Each host is pinged by its own small background process, which uses the
system's `ping`, so no special privileges are needed. Every result is kept in
`~/.config/pingy/hosts/<n>/samples`, up to 32 MB per host by default (about
two weeks at one ping a second); past that, the oldest quarter is dropped.
The panel shows how much it all takes; change the limit there with ctrl-o, or
with `pingy --history <size>`. `pingy --help` lists every key and option.

pingy shares its look with [macro-master](https://github.com/jessejwatson/macro-master):
the terminal's own 16 colours, rounded panels, and plain output when piped or
with `NO_COLOR`.

## Build and test

```sh
make test       # go vet + go test ./...
make build      # bin/pingy
make install    # copies bin/pingy to ~/.local/bin (PREFIX=... to change)
```

Requires Go, on macOS or Linux. Dependencies are
[lipgloss](https://github.com/charmbracelet/lipgloss),
[Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Bubbles](https://github.com/charmbracelet/bubbles).

## Try it without touching your real setup

```sh
export PINGY_HOME=$(mktemp -d)
bin/pingy 1.1.1.1 -d
```

## Homebrew

`Formula/pingy.rb` is the formula for the tap. After tagging a release
(`v0.1.9`), set `sha256` to the tarball's checksum:

```sh
curl -sL https://github.com/jessejwatson/pingy/archive/refs/tags/v0.1.9.tar.gz | shasum -a 256
```

## License

MIT. See [LICENSE](LICENSE).
