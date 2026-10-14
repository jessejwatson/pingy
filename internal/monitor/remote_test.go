package monitor

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpecsAndFindWithOrigins(t *testing.T) {
	if tg, o := SplitSpec("1.1.1.1@jesse@office.lan"); tg != "1.1.1.1" || o != "jesse@office.lan" {
		t.Fatalf("split = %q %q", tg, o)
	}
	root := t.TempDir()
	office, _ := Create(root, "1.1.1.1", "office", time.Second)
	here, _ := Create(root, "1.1.1.1", "", time.Second)
	if office.Label() != "1.1.1.1@office" || here.Label() != "1.1.1.1" {
		t.Fatalf("labels %q %q", office.Label(), here.Label())
	}
	if m := Find(root, "1.1.1.1"); m == nil || m.ID != here.ID {
		t.Fatalf("a bare target should find the one pinged from here: %v", m)
	}
	if m := Find(root, "1.1.1.1@office"); m == nil || m.ID != office.ID {
		t.Fatalf("find target@origin: %v", m)
	}
	if m := Find(root, "1.1.1.1@home"); m != nil {
		t.Fatalf("found %v from an origin it isn't pinged from", m)
	}
	here.Remove()
	if m := Find(root, "1.1.1.1"); m == nil || m.ID != office.ID {
		t.Fatalf("with none here, a bare target finds it elsewhere: %v", m)
	}
	if m := FindExact(root, "1.1.1.1"); m != nil {
		t.Fatalf("FindExact found %v, which isn't pinged from here", m)
	}
}

func TestChecksKeepSSHSafe(t *testing.T) {
	for _, bad := range []string{"-oProxyCommand=x", "a b", ""} {
		if CheckDest(bad) == nil {
			t.Errorf("dest %q was allowed", bad)
		}
	}
	for _, bad := range []string{"x;rm -rf ~", "$(id)", "-c", "a'b"} {
		if CheckRemoteTarget(bad) == nil {
			t.Errorf("target %q was allowed", bad)
		}
	}
	for _, ok := range []string{"1.1.1.1", "router.lan", "fe80::1%en0", "my_host"} {
		if err := CheckRemoteTarget(ok); err != nil {
			t.Errorf("target %q: %v", ok, err)
		}
	}
	if CheckOriginName("a@b") == nil || CheckOriginName("office") != nil {
		t.Error("origin names")
	}
	remote, _ := RemoteCommand("it's", time.Second, time.Time{})
	if remote != `sh -s -- 'it'\''s' '1000' '0'` {
		t.Errorf("quoted as %s", remote)
	}
}

func TestParseStreamLine(t *testing.T) {
	cases := map[string]streamLine{
		"pingy-stream 1 pingy": {kind: 'h', text: "pingy"},
		"= 1.1.1.1":            {kind: '=', text: "1.1.1.1"},
		"! can't resolve x":    {kind: '!', text: "can't resolve x"},
		"!":                    {kind: '!'},
		"1700000000000 1500":   {kind: 's', sample: Sample{At: time.UnixMilli(1700000000000), RTT: 1500 * time.Microsecond}},
		"- -1":                 {kind: 's', sample: Sample{RTT: -1}, now: true},
		"- 2000\r":             {kind: 's', sample: Sample{RTT: 2 * time.Millisecond}, now: true},
	}
	for in, want := range cases {
		got, ok := parseStreamLine(in)
		if !ok || got != want {
			t.Errorf("%q = %+v, %v; want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"pingy-stream 2 pingy", "hello", "12 x", ""} {
		if _, ok := parseStreamLine(bad); ok {
			t.Errorf("%q was read", bad)
		}
	}
}

func TestFollowSendsWhatsNewAcrossATrim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples")
	os.WriteFile(path, []byte("1000 5\n2000 6\n3000 7\n"), 0o600)
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	polls := 0
	Follow(path, time.UnixMilli(1000), w, time.Millisecond, func() bool {
		polls++
		switch polls {
		case 1:
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			f.WriteString("4000 8\n5000")
			f.Close()
		case 2:
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			f.WriteString(" 9\n")
			f.Close()
		case 3:
			// Trimmed: shorter, with lines already sent and one new.
			os.WriteFile(path, []byte("4000 8\n5000 9\n6000 1\n"), 0o600)
		}
		return polls < 4
	})
	if want := "2000 6\n3000 7\n4000 8\n5000 9\n6000 1\n"; out.String() != want {
		t.Fatalf("sent %q, want %q", out.String(), want)
	}
}

func TestLastSampleTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples")
	if !lastSampleTime(path).IsZero() {
		t.Fatal("no file should be no time")
	}
	os.WriteFile(path, []byte(strings.Repeat("1000 5\n", 50)+"2000 -1\n"), 0o600)
	if got := lastSampleTime(path); got.UnixMilli() != 2000 {
		t.Fatalf("last = %v", got.UnixMilli())
	}
}

// TestRunRemote streams from a stand-in for ssh: a script that answers as
// remote pingy would, then hangs up, so the monitor connects again and
// skips the pings it already has.
func TestRunRemote(t *testing.T) {
	home := t.TempDir()
	fake := filepath.Join(home, "fakessh")
	os.WriteFile(fake, []byte(`#!/bin/sh
echo "$@" >> "$(dirname "$0")/calls"
echo "pingy-stream 1 pingy"
echo "= 9.9.9.9"
echo "1000 1500"
echo "2000 -1"
echo "- 3000"
`), 0o700)
	t.Setenv("PINGY_SSH", fake)
	m, err := Create(filepath.Join(home, "hosts"), "dns.example", "office", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	up := newUpkeep(m.Dir())
	out, err := openSamples(m.SamplesPath(), up.limit)
	if err != nil {
		t.Fatal(err)
	}
	defer out.close()
	stop := make(chan os.Signal)
	done := make(chan error)
	go func() { done <- runRemote(m, up, out, State{}, stop, nil) }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(home, "calls"))
		if strings.Count(string(b), "\n") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("didn't connect twice")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h := NewHistory(m.SamplesPath(), 0)
	h.Refresh()
	s := h.Samples
	// 1000 and 2000 once, as they came the second time too; a "now" ping
	// each time.
	if len(s) != 4 || s[0].At.UnixMilli() != 1000 || s[0].RTT != 1500*time.Microsecond || !s[1].Lost() ||
		s[2].RTT != 3*time.Millisecond || !s[3].At.After(s[2].At) {
		t.Fatalf("samples %+v", s)
	}
	st := m.State()
	if st.Via != "pingy" || st.Addr != "9.9.9.9" {
		t.Fatalf("state %+v", st)
	}
	calls, _ := os.ReadFile(filepath.Join(home, "calls"))
	first, second, _ := strings.Cut(string(calls), "\n")
	if !strings.Contains(first, "-- office sh -s -- 'dns.example' '1000' '0'") || !strings.Contains(second, "'1000' '") ||
		strings.Contains(second, "'1000' '0'") {
		t.Fatalf("ssh was run as:\n%s", calls)
	}
}

func TestRemoteScriptParses(t *testing.T) {
	for _, sh := range []string{"sh", "dash", "bash"} {
		if _, err := exec.LookPath(sh); err != nil {
			continue
		}
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(remoteScript)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, out)
		}
	}
}

func TestLoginNeeded(t *testing.T) {
	for stderr, want := range map[string]string{
		"No ED25519 host key is known for x and you have requested strict checking.\nHost key verification failed.\n": LoginHostKey,
		"@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n...\nHost key verification failed.\n":     LoginChanged,
		"jesse@x: Permission denied (publickey,password).\n":                                                          LoginAuth,
		"ssh: connect to host x port 22: Connection refused\n":                                                        "",
		"ssh: Could not resolve hostname x: nodename nor servname provided\n":                                         "",
	} {
		if got := LoginNeeded(stderr); got != want {
			t.Errorf("%q = %q, want %q", stderr, got, want)
		}
	}
}

func TestSSHSharesALogin(t *testing.T) {
	home := t.TempDir()
	short := "/tmp/" + filepath.Base(home)
	os.MkdirAll(short, 0o700)
	defer os.RemoveAll(short)
	for _, h := range []string{short, home + strings.Repeat("/deeper", 12)} {
		cp := controlPath(h)
		if cp == "" || len(cp) > 100 || !strings.HasSuffix(cp, "/%C") {
			t.Fatalf("control path for %s = %q", h, cp)
		}
	}
	cmd := SSH(short, "office", "true", nil)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "BatchMode=yes") || !strings.Contains(args, "ControlMaster=no") ||
		!strings.Contains(args, "ControlPath="+short+"/ssh/%C") || !strings.HasSuffix(args, "-- office true") {
		t.Fatalf("ssh %s", args)
	}
	login := strings.Join(Login(short, "office").Args, " ")
	if strings.Contains(login, "BatchMode") || !strings.Contains(login, "ControlMaster=yes") ||
		!strings.Contains(login, "ControlPersist=yes") || !strings.Contains(login, "-f -N") || !strings.Contains(login, "ServerAliveInterval") {
		t.Fatalf("login ssh %s", login)
	}
}

// TestRetryCutsTheWaitShort: a monitor waiting to connect again tries at
// once when told to.
func TestRetryCutsTheWaitShort(t *testing.T) {
	home := t.TempDir()
	fake := filepath.Join(home, "fakessh")
	os.WriteFile(fake, []byte("#!/bin/sh\necho x >> \"$(dirname \"$0\")/calls\"\necho 'Permission denied (password).' >&2\nexit 255\n"), 0o700)
	t.Setenv("PINGY_SSH", fake)
	m, _ := Create(filepath.Join(home, "hosts"), "1.1.1.1", "office", time.Second)
	up := newUpkeep(m.Dir())
	out, _ := openSamples(m.SamplesPath(), up.limit)
	defer out.close()
	stop, retry := make(chan os.Signal), make(chan os.Signal, 1)
	done := make(chan error)
	go func() { done <- runRemote(m, up, out, State{}, stop, retry) }()
	calls := func() int { b, _ := os.ReadFile(filepath.Join(home, "calls")); return strings.Count(string(b), "\n") }
	for deadline := time.Now().Add(5 * time.Second); calls() < 1; {
		if time.Now().After(deadline) {
			t.Fatal("never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // into the 2s wait
	if st := m.State(); st.Login != LoginAuth || st.Link == "" {
		t.Fatalf("state %+v", st)
	}
	retry <- os.Interrupt
	time.Sleep(500 * time.Millisecond)
	if n := calls(); n != 2 {
		t.Fatalf("%d connections, want 2 straight after the retry", n)
	}
	stop <- os.Interrupt
	<-done
}

// TestStalledConnectionIsGivenUp: a connection that goes quiet is cut off
// and tried again, and one that never answers ends the login it went
// through, so the next try finds out it must log in again.
func TestStalledConnectionIsGivenUp(t *testing.T) {
	defer func(q, u time.Duration) { quietFor, unansweredFor = q, u }(quietFor, unansweredFor)
	quietFor, unansweredFor = 500*time.Millisecond, time.Second
	home := t.TempDir()
	fake := filepath.Join(home, "fakessh")
	// -O exit is CloseLogin; the first connection answers then hangs, the
	// rest hang without a word.
	os.WriteFile(fake, []byte(`#!/bin/sh
dir=$(dirname "$0")
case " $* " in *" -O exit "*) echo exit >> "$dir/closed"; exit 0 ;; esac
echo x >> "$dir/calls"
if [ "$(wc -l < "$dir/calls")" -eq 1 ]; then
	echo "pingy-stream 1 pingy"
	echo "- 1000"
fi
exec sleep 60
`), 0o700)
	t.Setenv("PINGY_SSH", fake)
	m, _ := Create(filepath.Join(home, "hosts"), "1.1.1.1", "office", MinInterval)
	up := newUpkeep(m.Dir())
	out, _ := openSamples(m.SamplesPath(), up.limit)
	defer out.close()
	stop := make(chan os.Signal)
	done := make(chan error)
	go func() { done <- runRemote(m, up, out, State{}, stop, nil) }()
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(home, name)); return string(b) }
	for deadline := time.Now().Add(15 * time.Second); read("closed") == ""; {
		if time.Now().After(deadline) {
			t.Fatalf("login never ended; connected %d times, state %+v", strings.Count(read("calls"), "\n"), m.State())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := strings.Count(read("calls"), "\n"); n != 2 {
		t.Fatalf("%d connections before ending the login, want 2", n)
	}
	if st := m.State(); st.Link != "no answer from office" {
		t.Fatalf("state %+v", st)
	}
	stop <- os.Interrupt
	<-done
}
