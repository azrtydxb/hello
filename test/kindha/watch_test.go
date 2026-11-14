package kindha

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// What the lab records while a scenario runs, so that a failure carries the
// evidence a post-mortem needs: a pod that a scenario deletes takes its logs
// with it (the kubelet drops them within a minute or so), and Kamailio at
// debug=2 does not print the messages it handles. The record is logged only
// when the scenario fails.

// stream is the output of one background command, kept in memory.
type stream struct {
	name string
	mu   sync.Mutex
	buf  bytes.Buffer
	cmd  *exec.Cmd
}

func (s *stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len() < 4<<20 { // a scenario is seconds long; this only bounds a runaway
		s.buf.Write(p)
	}
	return len(p), nil
}

// watch is a set of background commands recording one scenario.
type watch struct {
	streams []*stream
}

func (w *watch) start(name string, cmd *exec.Cmd) {
	s := &stream{name: name, cmd: cmd}
	cmd.Stdout, cmd.Stderr = s, s
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(s, "could not start: %v\n", err)
		cmd.Process = nil
	}
	w.streams = append(w.streams, s)
}

// stop ends the commands and waits for their output to land.
func (w *watch) stop() {
	for _, s := range w.streams {
		if s.cmd.Process != nil {
			// The whole group: sudo, timeout and tcpdump alike.
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGINT)
		}
	}
	for _, s := range w.streams {
		if s.cmd.Process == nil {
			continue
		}
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
}

func (w *watch) dump(t *testing.T) {
	t.Helper()
	for _, s := range w.streams {
		s.mu.Lock()
		out := s.buf.String()
		s.mu.Unlock()
		t.Logf("=== %s\n%s", s.name, strings.TrimRight(out, "\n"))
	}
}

// watchEdge records the edge while a pod is deleted: the Kamailio (at message
// level, set for the duration of the scenario) and kube-vip logs of every pod,
// streamed from now on, and the SIP datagrams between the runner and the nodes
// as the runner's bridge sees them (when tcpdump can be run). Call it before
// the loss; the returned function stops the recording and logs it if the test
// failed.
func (l *labEnv) watchEdge(t *testing.T) *watch {
	t.Helper()
	w := &watch{}
	for _, n := range l.nodes {
		pods, _ := l.tryKubectl("-n", l.namespace, "get", "pods", "-l", "app.kubernetes.io/component=edge",
			"--field-selector", "spec.nodeName="+n, "-o", "jsonpath={.items[*].metadata.name}")
		for _, p := range strings.Fields(pods) {
			// debug=3 makes Kamailio print each message it receives, every
			// transaction's relay and its send errors, which is how a
			// request that was answered with a 100 and never relayed shows.
			if out, err := l.tryKubectl("-n", l.namespace, "exec", p, "-c", "kamailio", "--", "kamcmd", "cfg.set_now_int", "core", "debug", "3"); err != nil {
				t.Logf("could not raise %s's log level: %v %s", p, err, strings.TrimSpace(out))
			}
			for _, c := range []string{"kamailio", "vip"} {
				args := append(append([]string{}, l.kubectlArgs...), "-n", l.namespace, "logs", "-f", "--timestamps", "--since=1s", p, "-c", c)
				w.start(fmt.Sprintf("%s %s/%s log (from the loss)", n, p, c), exec.Command("kubectl", args...)) //nolint:gosec // test tooling
			}
		}
	}
	// What crosses the runner's bridge: the phones, Hello and the nodes.
	if dev := l.vipDevice(); dev != "" {
		filter := "udp and (host " + l.vip
		for _, n := range l.nodes {
			filter += " or host " + l.nodeIPs[n]
		}
		filter += ")"
		w.start("tcpdump on "+dev+" (runner)", exec.Command("sudo", "-n", "timeout", "120", "tcpdump", "-l", "-n", "-tt", "-s", "0", "-A", "-i", dev, filter)) //nolint:gosec // test tooling
	}
	t.Cleanup(func() {
		w.stop()
		if t.Failed() {
			w.dump(t)
		}
	})
	return w
}

// vipDevice is the runner's interface towards the VIP.
func (l *labEnv) vipDevice() string {
	out, err := tryRun("ip", "-o", "route", "get", l.vip)
	if err != nil {
		return ""
	}
	fields := strings.Fields(out)
	for i, w := range fields {
		if w == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
