package models

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func sha256Bytes(b []byte) string    { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func stringsHasSherpa(p string) bool { return strings.HasPrefix(p, "sherpa/") }
func (m *Manager) launchLocked(id string) error {
	if m.running[id] || m.ctx.Err() != nil || m.jobs[id].Deferred {
		return nil
	}
	if err := m.admitLocked(id); err != nil {
		return err
	}
	if m.jobs[id].State == "paused" {
		return nil
	}
	m.running[id] = true
	ctx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	m.jobCancels[id] = cancel
	m.jobDone[id] = done
	e, _ := m.entry(id)
	j := *m.jobs[id]
	go func() {
		defer close(done)
		defer cancel()
		err := m.download(ctx, e, j.Source)
		m.mu.Lock()
		m.running[id] = false
		delete(m.jobCancels, id)
		delete(m.jobDone, id)
		delete(m.reserved, id)
		job := m.jobs[id]
		if err != nil && job.PauseReason != "" {
			job.State = "paused"
		} else if err != nil {
			job.State = "failed"
			job.Error = err.Error()
		} else {
			job.State = "done"
			job.BytesDone = job.BytesTotal
			job.Error = ""
			job.PauseReason = ""
		}
		if m.ctx.Err() != nil {
			job.State = "queued"
			job.Error = ""
		}
		if saveErr := m.saveLocked(); saveErr != nil {
			job.State = "failed"
			job.Error = saveErr.Error()
		}
		if job.State == "paused" && m.network.pauseReason() == "" && m.ctx.Err() == nil {
			_ = m.launchLocked(id)
		}
		m.changedLocked()
		handler := m.onComplete
		completed := job.State == "done" && handler != nil
		if completed {
			m.completions.Add(1)
		}
		m.mu.Unlock()
		if completed {
			defer m.completions.Done()
			handler(m.ctx, e)
		}
	}()
	return nil
}
func (m *Manager) download(ctx context.Context, e Entry, source Source) error {
	m.mu.Lock()
	m.jobs[e.ID].State = "downloading"
	m.changedLocked()
	m.mu.Unlock()
	if presentAt(m.folder, m.legacy, e) {
		return nil
	}
	var done int64
	for _, f := range e.Files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dest := filepath.Join(m.folder, f.Path)

		if !recognized(dest, f.Bytes, f.SHA256) {
			prefix := resumablePrefix(dest, *f.Bytes, *f.SHA256)
			last := prefix
			remote := fileURL(e, f, source)
			err := transfer(ctx, remote, dest, *f.SHA256, *f.Bytes, func(n int64) {
				m.consumeReservation(e.ID, n-last)
				last = n
				m.mu.Lock()
				m.jobs[e.ID].BytesDone = done + n
				m.changedLocked()
				m.mu.Unlock()
			}, func(have int64) error {
				if have == 0 && prefix > 0 {
					last = 0
					return m.restoreReservation(e.ID, prefix)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
		}
		if f.Archive != nil {
			if err := installArchive(ctx, dest, m.folder, f.Archive, func(n int64) { m.consumeReservation(e.ID, n) }); err != nil {
				return err
			}
			_ = os.Remove(dest)
		}
		done += *f.Bytes
		m.mu.Lock()
		m.jobs[e.ID].BytesDone = done
		m.changedLocked()
		m.mu.Unlock()
	}
	return nil
}
func fileURL(e Entry, f File, s Source) string {
	for i, source := range e.Sources {
		if source == s {
			return f.URLs[i]
		}
	}
	// A custom mirror reproduces the canonical mirror's relative layout.
	suffix := strings.TrimPrefix(f.URLs[0], e.Sources[0].URL)
	return strings.TrimRight(s.URL, "/") + "/" + strings.TrimLeft(suffix, "/")
}

// transfer never publishes unverified bytes. Partial files survive interruptions;
// a strong validator and the pinned full-file checksum make range resumes safe.
//
//isoform:destination metadata
func transfer(parent context.Context, remote, dest, want string, bytes int64, progress func(int64), prepare ...func(int64) error) error {
	if len(want) != 64 {
		return errors.New("download requires a manifest checksum")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	idle := time.AfterFunc(90*time.Second, cancel)
	defer idle.Stop()
	tmp := dest + ".partial"
	validatorFile := tmp + ".validator"
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}
	validator, _ := os.ReadFile(validatorFile)
	if have >= bytes && have > 0 {
		got, err := hashFile(tmp)
		if err == nil && got == want {
			return os.Rename(tmp, dest)
		}
		_ = os.Remove(tmp)
		have = 0
	}
	if len(validator) == 0 {
		have = 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remote, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		req.Header.Set("If-Range", string(validator))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		have = 0
		v := resp.Header.Get("ETag")
		if v == "" || strings.HasPrefix(v, "W/") {
			v = resp.Header.Get("Last-Modified")
		}
		if v != "" {
			if err := os.WriteFile(validatorFile, []byte(v), 0600); err != nil {
				return err
			}
		} else {
			_ = os.Remove(validatorFile)
		}
	case http.StatusPartialContent:
		var start, end, total int64
		if have == 0 {
			return errors.New("unexpected partial response")
		}
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != have || total != bytes || end < start {
			return errors.New("invalid download range")
		}
	case http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(tmp)
		_ = os.Remove(validatorFile)
		return errors.New("resume range rejected; retry to start over")
	default:
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if have > 0 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	out, err := os.OpenFile(tmp, flags, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, check := range prepare {
		if err := check(have); err != nil {
			return err
		}
	}
	h := sha256.New()
	if have > 0 {
		in, err := os.Open(tmp)
		if err != nil {
			return err
		}
		_, err = io.CopyN(h, in, have)
		in.Close()
		if err != nil {
			return err
		}
	}
	buf := make([]byte, 128*1024)
	n := have
	for {
		read, readErr := resp.Body.Read(buf)
		if read > 0 {
			idle.Reset(90 * time.Second)
			n += int64(read)
			if n > bytes {
				_ = os.Remove(tmp)
				return errors.New("download exceeds manifest size")
			}
			if _, err := out.Write(buf[:read]); err != nil {
				return err
			}
			h.Write(buf[:read])
			progress(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if n != bytes {
		return fmt.Errorf("incomplete download: %d of %d bytes", n, bytes)
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		_ = os.Remove(tmp)
		_ = os.Remove(validatorFile)
		return errors.New("checksum mismatch")
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	_ = os.Remove(validatorFile)
	return nil
}
func installArchive(ctx context.Context, path, folder string, a *Archive, progress ...func(int64)) error {
	target := filepath.Join(folder, a.Destination)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".model-install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(bzip2.NewReader(f))
	allowed := map[string]InstalledFile{}
	for _, member := range a.Files {
		allowed[member.Path] = member
	}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported archive member: %s", header.Name)
		}
		relative := strings.TrimPrefix(header.Name, a.Root+"/")
		inside, ok := allowed[relative]
		if !ok || !safePath(relative) || header.Size != inside.Bytes {
			return fmt.Errorf("unlisted archive member: %s", header.Name)
		}
		if seen[relative] {
			return errors.New("duplicate archive member")
		}
		seen[relative] = true
		p := filepath.Join(staging, relative)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(&countedWriter{writer: out, progress: progress}, h), &contextReader{ctx: ctx, reader: tr})
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(h.Sum(nil)) != inside.SHA256 {
			return fmt.Errorf("archive member checksum mismatch: %s", relative)
		}
	}
	if len(seen) != len(allowed) {
		return errors.New("incomplete model archive")
	}
	// Never remove a usable installation until the replacement is complete.
	if err := ctx.Err(); err != nil {
		return err
	}
	backup := ""
	if _, err := os.Stat(target); err == nil {
		slot, err := os.MkdirTemp(filepath.Dir(target), ".model-previous-*")
		if err != nil {
			return err
		}
		if err := os.Remove(slot); err != nil {
			return err
		}
		backup = slot
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type countedWriter struct {
	writer   io.Writer
	progress []func(int64)
}

func (w *countedWriter) Write(b []byte) (int, error) {
	n, err := w.writer.Write(b)
	for _, progress := range w.progress {
		progress(int64(n))
	}
	return n, err
}
