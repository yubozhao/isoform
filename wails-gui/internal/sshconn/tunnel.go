package sshconn

import (
	"fmt"
	"io"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Tunnel implements local port forwarding over an SSH connection.
// It listens on 127.0.0.1:0 (random port) and forwards traffic to
// 127.0.0.1:remotePort on the remote host.
type Tunnel struct {
	client     *ssh.Client
	remoteAddr string
	listener   net.Listener
	localPort  int
	done       chan struct{}
	closeOnce  sync.Once
}

// LocalPort returns the local port the tunnel is listening on.
func (t *Tunnel) LocalPort() int {
	return t.localPort
}

// Start begins accepting connections and forwarding them through SSH.
// This method blocks until Close is called or an error occurs.
func (t *Tunnel) Start() error {
	defer t.Close()

	for {
		local, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.done:
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		go t.forward(local)
	}
}

// Close stops the tunnel and releases all resources.
func (t *Tunnel) Close() error {
	var err error
	t.closeOnce.Do(func() {
		close(t.done)
		err = t.listener.Close()
	})
	return err
}

// forward handles a single connection by dialing the remote and copying
// data in both directions.
func (t *Tunnel) forward(local net.Conn) {
	remote, err := t.client.Dial("tcp", t.remoteAddr)
	if err != nil {
		local.Close()
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(remote, local)
		remote.Close()
	}()

	go func() {
		defer wg.Done()
		io.Copy(local, remote)
		local.Close()
	}()

	wg.Wait()
}
