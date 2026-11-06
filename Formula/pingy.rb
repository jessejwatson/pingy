class Pingy < Formula
  desc "Ping many hosts at once in the background, and graph them in the terminal"
  homepage "https://github.com/jessejwatson/pingy"
  url "https://github.com/jessejwatson/pingy/archive/refs/tags/v0.1.14.tar.gz"
  sha256 "fbada00ec8bd13d86fc9542183c219dafc5a576796478b6d3a408480d966552c"
  license "MIT"
  head "https://github.com/jessejwatson/pingy.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}", output: bin/"pingy"), "./cmd/pingy"
  end

  def caveats
    <<~EOS
      `pingy <host> -d` pings a host in the background; `pingy` opens the
      panel to search hosts and see their graphs, `pingy -l` lists them and
      `pingy -g` graphs them all.

      `pingy -p` pauses every host, keeping their history. Background pings
      also stop when you log out or restart; `pingy -r` starts them all again,
      or `pingy --autostart on` has them start again by themselves at login.
    EOS
  end

  test do
    assert_match "pingy #{version}", shell_output("#{bin}/pingy --version")
    assert_match "pingy -g", shell_output("#{bin}/pingy --help")
    ENV["PINGY_HOME"] = testpath/"pingy"
    assert_match "Nothing is being pinged", shell_output("#{bin}/pingy -l 2>&1")
    assert_match "unknown option", shell_output("#{bin}/pingy --nope 2>&1", 1)
  end
end
