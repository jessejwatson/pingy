# pingy

Keep an eye on many pings at once. Start pinging a host in the background
with `pingy <host> -d`, as many as you like, then list them with `pingy -l`,
graph them all with `pingy -g`, or run `pingy` for a panel where you search
the hosts and see each one's recent graph. Opening a host shows its whole
history, which you can move back through, zoom, rename, pause or delete.

To watch a few hosts together, press ctrl-t in the panel, tab to pick them and
enter to graph them; press `w` there to save them as a view, which
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
```

Each host is pinged by its own small background process, which uses the
system's `ping`, so no special privileges are needed. Every result is kept in
`~/.config/pingy/hosts/<n>/samples` (about 15 days at one ping a second
before the oldest half is dropped). `pingy --help` lists every key and option.

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
(`v0.1.4`), set `sha256` to the tarball's checksum:

```sh
curl -sL https://github.com/jessejwatson/pingy/archive/refs/tags/v0.1.4.tar.gz | shasum -a 256
```

## License

MIT. See [LICENSE](LICENSE).
