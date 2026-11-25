package transport

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/procx"
)

// TestHelperProcessSleep is the tests' "remote process": it only does something when another test starts it.
func TestHelperProcessSleep(t *testing.T) {
	if os.Getenv("GITSI_HELPER_SLEEP") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

// Close is called at once by cancellation, the ACK reader and the command's defer: it must be safe and leave the process ended.
func TestStreamCloseIsSafeFromManyGoroutines(t *testing.T) {
	cmd := procx.Background(exec.Command(os.Args[0], "-test.run=^TestHelperProcessSleep$"))
	cmd.Env = append(os.Environ(), "GITSI_HELPER_SLEEP=1")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	st := &sshStream{cmd: cmd, in: in, out: out, stderr: &lockedBuffer{}}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = st.Close() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Close did not finish")
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() && cmd.ProcessState.String() == "" {
		t.Fatal("the process must have ended")
	}
	if err := st.Close(); err != nil { // and once more, afterwards: still harmless
		t.Fatal(err)
	}
}
