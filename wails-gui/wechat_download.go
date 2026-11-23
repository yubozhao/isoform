package main

import (
	"context"
	"crypto/aes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"wails-gui/internal/destinations"
)

// wechatMaxDownloadBytes caps the ciphertext size we're willing to pull from the
// CDN. Sized at ~2x wechatMaxImageBytes so AES padding + format overhead still
// fits comfortably, but a stalled or hostile endpoint can't force unbounded
// allocation via a large Content-Length or trickled body.
const wechatMaxDownloadBytes = 2 * wechatMaxImageBytes

// isAllowedWeChatCDNHost gates outbound GETs to hosts under the expected WeChat
// CDN family. Inbound message JSON supplies this URL, so without a host check
// `DownloadAndDecryptMedia` would be an SSRF primitive if the iLink Bot channel
// were ever compromised.
func isAllowedWeChatCDNHost(host string) bool {
	host = strings.ToLower(host)
	return strings.HasSuffix(host, ".weixin.qq.com") || strings.HasSuffix(host, ".qq.com")
}

// DownloadAndDecryptMedia fetches the encrypted bytes from a WeChat CDN URL
// and decrypts them with AES-128-ECB. Returns plaintext bytes plus a best-effort
// MIME type detected from the leading magic bytes.
//
// Reference: qufei1993/cc-weixin (plugins/weixin/src/media.ts). The response
// body is AES-128-ECB ciphertext with PKCS7 padding — symmetric with our upload
// flow in wechat_send.go. Prefers `FullURL` (already encodes the query param);
// falls back to `URL + ?encrypted_query_param=...` otherwise.
//
//isoform:destination chat_bridges
func (c *wechatAPIClient) DownloadAndDecryptMedia(ctx context.Context, media *wechatMediaRef) ([]byte, string, error) {
	if media == nil {
		return nil, "", fmt.Errorf("nil media")
	}
	if media.AESKey == "" {
		return nil, "", fmt.Errorf("empty aes_key")
	}

	downloadURL := media.FullURL
	if downloadURL == "" {
		if media.URL == "" {
			return nil, "", fmt.Errorf("empty media URL")
		}
		downloadURL = media.URL
		if media.EncryptQueryParam != "" {
			sep := "?"
			if strings.Contains(downloadURL, "?") {
				sep = "&"
			}
			downloadURL += sep + "encrypted_query_param=" + url.QueryEscape(media.EncryptQueryParam)
		}
	}

	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse media URL: %w", err)
	}
	// Hostname() strips any :port so a legitimate host:443 URL doesn't trip
	// the suffix check. CheckRedirect on mediaClient re-applies this allowlist
	// to every redirect hop so a 30x can't bypass it.
	if parsed.Scheme != "https" || !isAllowedWeChatCDNHost(parsed.Hostname()) {
		return nil, "", fmt.Errorf("media URL host not allowed: %s", parsed.Host)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build CDN download request: %w", err)
	}

	req = destinations.CountRequest(req, destinations.ChatBridges)
	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("CDN download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("CDN download status %d: %s", resp.StatusCode, string(body))
	}

	// Bound ciphertext reads so a hostile/misbehaving CDN can't force OOM
	// before we ever get to the post-decryption size check.
	encrypted, err := io.ReadAll(io.LimitReader(resp.Body, wechatMaxDownloadBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read CDN response: %w", err)
	}
	if int64(len(encrypted)) > wechatMaxDownloadBytes {
		return nil, "", fmt.Errorf("CDN response exceeds %d bytes", wechatMaxDownloadBytes)
	}

	keyBytes, err := decodeAESKey(media.AESKey)
	if err != nil {
		return nil, "", fmt.Errorf("decode aes_key: %w", err)
	}

	plaintext, err := decryptAESECB(encrypted, keyBytes)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt media: %w", err)
	}

	return plaintext, detectImageMIME(plaintext), nil
}

// decryptAESECB decrypts ciphertext with AES-128-ECB and strips PKCS7 padding.
func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d not positive multiple of block size", len(ciphertext))
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return pkcs7Unpad(plaintext, aes.BlockSize)
}

// pkcs7Unpad removes PKCS#7 padding from data.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	if len(data)%blockSize != 0 {
		return nil, fmt.Errorf("data length %d not multiple of block size", len(data))
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize {
		return nil, fmt.Errorf("invalid padding length %d", pad)
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return nil, fmt.Errorf("invalid padding bytes")
		}
	}
	return data[:len(data)-pad], nil
}

// detectImageMIME peeks at the leading bytes to identify common image formats.
// Falls back to "image/jpeg" (the most common WeChat image encoding).
func detectImageMIME(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A:
		return "image/png"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
