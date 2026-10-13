class Pingy < Formula
  desc "Ping many hosts at once in the background, and graph them in the terminal"
  homepage "https://github.com/jessejwatson/pingy"
  url "https://github.com/jessejwatson/pingy/archive/refs/tags/v0.1.2.tar.gz"
  sha256 "2c26ec4967e6030afd6da476cd527dbb475211f48d062c946ae57ca3b558e0ae"
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

      Background pings stop when you log out or restart; `pingy -r` starts
      them all again.
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
