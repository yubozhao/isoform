// Command tasklink-dogfood serves one test task link on loopback, the same
// handler the app uses (internal/tasklink), so a bridge (an ephemeral zrok
// share on the person's own account) and a real agent can be tried before
// the app wiring ships. The task is a fixed harmless test; no personal data.
//
//	go run ./cmd/tasklink-dogfood -addr 127.0.0.1:8793 -base https://<share>
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"wails-gui/internal/tasklink"
)

const testTask = "This is a test of Isoform task links. Reply with status done and a summary that says which agent you are and the current UTC date. Do not open any other link."

func main() {
	addr := flag.String("addr", "127.0.0.1:8793", "loopback address to serve on")
	base := flag.String("base", "", "public https origin that reaches addr (the bridge)")
	flag.Parse()
	if !strings.HasPrefix(*addr, "127.0.0.1:") {
		log.Fatal("serve on 127.0.0.1 only")
	}
	if !strings.HasPrefix(*base, "https://") {
		log.Fatal("-base must be the bridge's https origin")
	}
	store := tasklink.NewStore(tasklink.Hooks{
		Taken: func(l tasklink.Link) { log.Printf("TAKEN link=%s gets=%d", l.ID, l.Gets) },
		Resulted: func(l tasklink.Link, r tasklink.Result) {
			log.Printf("RESULT status=%s summary=%q links=%v", r.Status, r.Summary, r.Links)
			go func() { time.Sleep(time.Second); os.Exit(0) }()
		},
	})
	token, err := store.Create("dogfood-1", "any", testTask)
	if err != nil {
		log.Fatal(err)
	}
	link := strings.TrimRight(*base, "/") + tasklink.PathPrefix + token
	fmt.Println("LINK " + link)
	fmt.Println("PROMPT " + tasklink.Prompt(link))
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s ua=%q accept=%q", r.Method, r.URL.Path[:min(len(r.URL.Path), 8)]+"...", r.UserAgent(), r.Header.Get("Accept"))
		store.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: *addr, Handler: logged, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
