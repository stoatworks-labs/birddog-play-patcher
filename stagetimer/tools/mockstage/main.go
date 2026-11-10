// mockstage runs the mock Stagetimer (internal/mockstage) on a local port, for
// trying bdstage end to end without a Stagetimer account:
//
//	go run ./tools/mockstage -addr 127.0.0.1:8097
//	go run . --serve :8096 --display none --sink png:/tmp/frames --allow-unprotected
//	  (then save room "mock" and key "mock-key-0001" with API address
//	   http://127.0.0.1:8097 on the tab, or POST /api/config)
//
// Commands on stdin, one per line, act like the Stagetimer controller:
//
//	start | stop | next | left 90 | flash 3 | msg 0 | focus 1 | hide
//	blackout | onair | rename 1 New name | kick | block | unblock
//
// -script runs a fixed sequence instead, for a recorded end-to-end check.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stoatworks-labs/bd-play-stagetimer/internal/mockstage"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8097", "listen address")
	room := flag.String("room", "mock", "room ID the mock accepts")
	key := flag.String("key", "mock-key-0001", "API key the mock accepts")
	script := flag.Bool("script", false, "run the fixed sequence (about 40 s) and exit")
	flag.Parse()

	s := mockstage.New(*room, *key)
	s.PingInterval = 5 * time.Second
	go func() { log.Fatal(http.ListenAndServe(*addr, s.Handler())) }()
	log.Printf("mock Stagetimer on http://%s (room %q)", *addr, *room)

	if *script {
		runScript(s)
		return
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		if err := command(s, in.Text()); err != nil {
			log.Print(err)
		}
	}
	select {}
}

func command(s *mockstage.Server, line string) error {
	f := strings.Fields(line)
	if len(f) == 0 {
		return nil
	}
	arg := func(def int) int {
		if len(f) > 1 {
			if n, err := strconv.Atoi(f[1]); err == nil {
				return n
			}
		}
		return def
	}
	switch f[0] {
	case "start":
		s.Start()
	case "stop":
		s.Stop()
	case "next":
		s.Next()
	case "left":
		s.SetRemaining(time.Duration(arg(60)) * time.Second)
	case "flash":
		s.Flash(arg(3))
	case "msg":
		s.ShowMessage(arg(0), false)
	case "focus":
		s.ShowMessage(arg(0), true)
	case "hide":
		s.HideMessage()
	case "blackout":
		s.SetRoom(func(r *mockstage.Room) { r.Blackout = !r.Blackout })
	case "onair":
		s.SetRoom(func(r *mockstage.Room) { r.OnAir = !r.OnAir })
	case "rename":
		if len(f) < 3 {
			return fmt.Errorf("rename N name")
		}
		s.RenameTimer(arg(0), strings.Join(f[2:], " "))
	case "kick":
		s.Kick()
	case "block":
		s.SetBlocked(true)
	case "unblock":
		s.SetBlocked(false)
	default:
		return fmt.Errorf("unknown command %q", f[0])
	}
	log.Printf("> %s", line)
	return nil
}

func runScript(s *mockstage.Server) {
	steps := []struct {
		after time.Duration
		cmd   string
	}{
		{8 * time.Second, "start"},
		{3 * time.Second, "left 250"},
		{3 * time.Second, "left 50"},
		{3 * time.Second, "msg 0"},
		{3 * time.Second, "hide"},
		{2 * time.Second, "flash 2"},
		{3 * time.Second, "left 3"},
		{5 * time.Second, "stop"},
		{2 * time.Second, "onair"},
		{2 * time.Second, "focus 1"},
		{3 * time.Second, "hide"},
		{1 * time.Second, "rename 2 Panel: what changed"},
		{3 * time.Second, "next"},
		{2 * time.Second, "blackout"},
		{2 * time.Second, "blackout"},
		{2 * time.Second, "kick"},
		{4 * time.Second, ""},
	}
	for _, st := range steps {
		time.Sleep(st.after)
		if st.cmd != "" {
			if err := command(s, st.cmd); err != nil {
				log.Print(err)
			}
		}
	}
	log.Print("script done")
}
