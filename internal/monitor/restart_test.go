package monitor

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestExeWatchWaitsForAFinishedPingyThatRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pingy")
	os.WriteFile(path, []byte("old"), 0o700)
	fi, _ := os.Stat(path)
	runs := true
	w := &exeWatch{path: path, running: fi, ok: func(string) bool { return runs }}
	look := func() bool { w.next = time.Time{}; return w.changed() }
	if look() {
		t.Fatal("changed before anything did")
	}
	replace := func(b string) {
		os.WriteFile(path+".new", []byte(b), 0o700)
		os.Rename(path+".new", path)
	}
	replace("new")
	if look() {
		t.Fatal("restarted on first sight, before it could be finished")
	}
	replace("newer") // still being written
	if look() {
		t.Fatal("restarted while it was still changing")
	}
	if !look() {
		t.Fatal("didn't restart once it settled")
	}
	runs = false
	replace("broken")
	look()
	if look() || look() {
		t.Fatal("restarted into a pingy that doesn't run")
	}
}

func TestStableExeIsBrewsOptLink(t *testing.T) {
	if m := cellarRe.FindStringSubmatch("/opt/homebrew/Cellar/pingy/0.1.9/bin/pingy"); m == nil || m[1] != "/opt/homebrew" {
		t.Fatalf("cellar path read as %v", m)
	}
	if cellarRe.MatchString("/Users/x/.local/bin/pingy") {
		t.Fatal("a pingy outside Homebrew read as in it")
	}
}

// TestMonitorRestartsIntoANewPingy: replacing pingy restarts a running
// monitor as the new one, the same process, without letting go of its
// lock for a moment.
func TestMonitorRestartsIntoANewPingy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds pingy")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin", "pingy")
	build := exec.Command("go", "build", "-o", bin, "pingy/cmd/pingy")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	m, err := Create(filepath.Join(home, "hosts"), "127.0.0.1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "__monitor", m.Dir())
	cmd.Env = append(os.Environ(), "PINGY_HOME="+home, "PINGY_EXE_CHECK_MS=100")
	log, _ := os.Create(filepath.Join(home, "log"))
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Signal(syscall.SIGTERM); cmd.Wait() }()
	for deadline := time.Now().Add(5 * time.Second); m.State().PID != cmd.Process.Pid; {
		if time.Now().After(deadline) {
			t.Fatal("monitor never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := m.State()

	b, _ := os.ReadFile(bin)
	os.WriteFile(bin+".new", b, 0o700)
	time.Sleep(10 * time.Millisecond) // a newer modification time
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		if !m.Running() {
			out, _ := os.ReadFile(filepath.Join(home, "log"))
			t.Fatalf("let go of its lock while restarting; log:\n%s", out)
		}
		st := m.State()
		if !st.Started.Equal(before.Started) {
			if st.PID != before.PID {
				t.Fatalf("restarted as pid %d, not %d", st.PID, before.PID)
			}
			break
		}
		if time.Now().After(deadline) {
			out, _ := os.ReadFile(filepath.Join(home, "log"))
			t.Fatalf("never restarted; log:\n%s", out)
		}
		time.Sleep(5 * time.Millisecond)
	}
	out, _ := os.ReadFile(filepath.Join(home, "log"))
	t.Logf("log:\n%s", out)
}
