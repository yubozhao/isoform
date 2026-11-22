package toolserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// Relay is the hidden tool-shim's complete implementation. It carries opaque
// MCP messages and has no tools or authority policy of its own.
//
//isoform:destination local
func Relay(ctx context.Context, ticketPath string, stdin io.Reader, stdout io.Writer) error {
	raw, err := readPrivateFile(ticketPath)
	if err != nil {
		return err
	}
	var ticket Ticket
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return err
	}
	if ticket.SocketPath == "" || ticket.TokenFile == "" {
		return errors.New("invalid run ticket")
	}
	token, err := readPrivateFile(ticket.TokenFile)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", ticket.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := json.NewEncoder(conn).Encode(map[string]string{"token": strings.TrimSpace(string(token))}); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("tool server authentication: %w", err)
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(line, &response) != nil || !response.OK {
		return errors.New("tool server authentication failed")
	}
	_ = conn.SetDeadline(time.Time{})
	inputDone := make(chan error, 1)
	outputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, stdin)
		if unixConn, ok := conn.(*net.UnixConn); ok {
			_ = unixConn.CloseWrite()
		}
		inputDone <- err
	}()
	go func() { _, err := io.Copy(stdout, reader); outputDone <- err }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-outputDone:
			return err
		case err := <-inputDone:
			if err != nil {
				return err
			}
			inputDone = nil
		}
	}
}

func readPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, fmt.Errorf("run credential file is not private: %s", path)
	}
	return os.ReadFile(path)
}
