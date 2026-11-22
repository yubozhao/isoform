//go:build darwin

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/ocr"
)

type request struct {
	Observations []ocr.TextObservation `json:"observations"`
}

type response struct {
	Ready      bool                  `json:"ready,omitempty"`
	Detections []pii.DetectionResult `json:"detections,omitempty"`
	Error      string                `json:"error,omitempty"`
}

func main() {
	_ = os.Setenv("YANSU_PII_HELPER_CHILD", "1")

	configDir := flag.String("config-dir", "", "Yansu config directory")
	flag.Parse()
	if *configDir == "" {
		writeResponse(response{Error: "missing --config-dir"})
		return
	}

	detector, err := pii.NewDetector(*configDir, nil)
	if err != nil {
		writeResponse(response{Error: fmt.Sprintf("init detector: %v", err)})
		return
	}
	defer detector.Destroy()

	writeResponse(response{Ready: true})

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			writeResponse(response{Error: fmt.Sprintf("decode request: %v", err)})
			continue
		}
		detections := detector.Detect(req.Observations)
		if !detector.IsReady() {
			writeResponse(response{Error: "privacy inference failed"})
			return
		}
		writeResponse(response{Detections: detections})
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read request: %v\n", err)
	}
}

func writeResponse(resp response) {
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
	}
}
