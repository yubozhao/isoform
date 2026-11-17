package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// A relay that runs on this computer is reached on this computer.
//
// The phone needs a public address for the relay (a tunnel). The computer does
// not: when the relay runs beside it, going out through the tunnel and back
// only adds a place to fail. On 2026-11-16 a 2.8 MB recording part took longer
// than the tunnel's 60 seconds on that round trip and the computer kept
// nothing. ISOFORM_RELAY_LOCAL_URL names the relay's own loopback address;
// with it set, everything the computer asks of the relay (its connection, a
// phone's uploads, attachments, the health check) goes there. The address the
// phone is given in a pairing invitation stays the public one.

const mobileRelayLocalURLEnv = "ISOFORM_RELAY_LOCAL_URL"

// mobileRelayLocalOrigin is the loopback address of the relay, or "" when none
// is named. Only plain http on 127.0.0.1 or localhost counts: the computer's
// credential must not go to another machine because of this setting.
func mobileRelayLocalOrigin() string {
	raw := strings.TrimSpace(os.Getenv(mobileRelayLocalURLEnv))
	if raw == "" {
		return ""
	}
	origin, err := validateMobileRelayURL(raw)
	if err != nil {
		log.Printf("[mobile-relay] %s ignored: it is not a relay address", mobileRelayLocalURLEnv)
		return ""
	}
	if parsed, err := url.Parse(origin); err != nil || parsed.Scheme != "http" {
		log.Printf("[mobile-relay] %s ignored: it must be http on this computer", mobileRelayLocalURLEnv)
		return ""
	}
	return origin
}

// hubOrigin is where the computer itself reaches the relay.
func (c *mobileRelayClient) hubOrigin() string {
	if c.local != "" {
		return c.local
	}
	return c.origin
}

// A download from the relay is tried again, from where it stopped, until the
// time the caller allows is over. One slow or cut connection must not cost a
// recording part its turn.
var (
	mobileUploadRetryFirst  = time.Second
	mobileUploadRetryMax    = 15 * time.Second
	mobileUploadAttemptWait = 40 * time.Second
)

// errMobileUploadRetry marks a failed attempt that is worth another.
var errMobileUploadRetry = errors.New("the relay did not deliver the upload yet")

// downloadUpload reads a phone's sealed upload (GET /v1/hub/uploads/{id}).
func (c *mobileRelayClient) downloadUpload(ctx context.Context, uploadID string) ([]byte, error) {
	if !mobileValidUUID(uploadID) {
		return nil, errMobileRecordingInvalid
	}
	var (
		sealed   []byte
		modified string
		delay    = mobileUploadRetryFirst
	)
	for {
		var err error
		sealed, modified, err = c.downloadUploadOnce(ctx, uploadID, sealed, modified)
		if err == nil {
			if len(sealed) == 0 || len(sealed) > mobileAttachmentMaxBytes {
				return nil, errMobileAttachmentTooLarge
			}
			return sealed, nil
		}
		if !errors.Is(err, errMobileUploadRetry) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			// Not the part's fault: the phone tries again later (503), it does
			// not count a refusal.
			return nil, err
		case <-time.After(delay):
		}
		delay = min(delay*2, mobileUploadRetryMax)
	}
}

// downloadUploadOnce makes one request. `have` is what earlier attempts read;
// the request asks for the rest, and only if the upload is still the one those
// bytes came from (If-Range). It returns everything read so far.
func (c *mobileRelayClient) downloadUploadOnce(ctx context.Context, uploadID string, have []byte, modified string) ([]byte, string, error) {
	attempt, cancel := context.WithTimeout(ctx, mobileUploadAttemptWait)
	defer cancel()
	req, err := c.hubUploadNewRequest(attempt, http.MethodGet, uploadID)
	if err != nil {
		return have, modified, err
	}
	if len(have) > 0 && modified != "" {
		req.Header.Set("Range", "bytes="+strconv.Itoa(len(have))+"-")
		req.Header.Set("If-Range", modified)
	} else {
		have = nil
	}
	resp, err := mobileAttachmentHTTPClient.Do(req)
	if err != nil {
		return have, modified, fmt.Errorf("%w: could not reach the relay: %v", errMobileUploadRetry, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		have = nil
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusNotFound:
		return nil, "", errMobileRecordingGone
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// What was read no longer fits the upload: start over.
		return nil, "", fmt.Errorf("%w: the relay answered %d for the upload", errMobileUploadRetry, resp.StatusCode)
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusRequestTimeout:
		return have, modified, fmt.Errorf("%w: the relay answered %d for the upload", errMobileUploadRetry, resp.StatusCode)
	default:
		return nil, "", fmt.Errorf("the relay answered %d for the upload", resp.StatusCode)
	}
	modified = resp.Header.Get("Last-Modified")
	room := int64(mobileAttachmentMaxBytes + 1 - len(have))
	more, err := io.ReadAll(io.LimitReader(resp.Body, room))
	have = append(have, more...)
	if err != nil {
		return have, modified, fmt.Errorf("%w: the download stopped after %d bytes: %v", errMobileUploadRetry, len(have), err)
	}
	return have, modified, nil
}
