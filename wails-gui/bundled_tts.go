package main

import (
	"archive/tar"
	"compress/bzip2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"wails-gui/models"
)

// MeloTTS zh_en is a single multilingual (Chinese + English) VITS voice. We
// download the official sherpa-onnx release tarball (~159 MB) and extract it
// into the sherpa data dir. Mirror order: the release R2 bucket
// (release.yansu.app, populated and fast everywhere) first; the dedicated
// model-assets R2 bucket second (canonical model home — falls through
// transparently while it 404s until ops populates it); GitHub release last.
// Same pattern as the ASR assets.
var meloTtsMirrorURLs = []string{
	"https://release.yansu.app/sherpa/tts/vits-melo-tts-zh_en.tar.bz2",
	"https://model-assets.yansu.app/sherpa/tts/vits-melo-tts-zh_en.tar.bz2",
	"https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-melo-tts-zh_en.tar.bz2",
}

const (
	envMeloTtsURL = "YANSU_SHERPA_TTS_MELO_URL"

	// SHA-256 of the canonical upstream tarball, captured from the GitHub
	// release on 2026-06-08. Verified before extraction so a corrupt mirror
	// or partial download can't reach disk. The mirror must serve identical
	// bytes for verification to pass.
	sha256MeloTtsArchive = "e58351ed7149f290a54534538badd4077cdbe6fddc964b24d0bee870415d1514"

	// Directory name inside the tarball.
	meloTtsDirName = "vits-melo-tts-zh_en"
)

// Kokoro is a high-quality English voice. We route English-dominant replies to
// it (MeloTTS zh_en handles Chinese); see dominantSpeechLang. Kokoro uses
// espeak-ng-data for g2p and does NOT need dict_dir, so it sidesteps the
// binding's unmapped-dict_dir limitation that hurts Chinese.
var kokoroEnMirrorURLs = []string{
	"https://release.yansu.app/sherpa/tts/kokoro-en-v0_19.tar.bz2",
	"https://model-assets.yansu.app/sherpa/tts/kokoro-en-v0_19.tar.bz2",
	"https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-en-v0_19.tar.bz2",
}

const (
	envKokoroEnURL        = "YANSU_SHERPA_TTS_KOKORO_URL"
	sha256KokoroEnArchive = "912804855a04745fa77a30be545b3f9a5d15c4d66db00b88cbcd4921df605ac7"
	kokoroEnDirName       = "kokoro-en-v0_19"
)

// ttsPaths describes where the MeloTTS model files live on disk. Defined here
// (cross-platform, no cgo) so both the tagged engine and the stub can refer to
// it without duplication.
type ttsPaths struct {
	root      string // ~/.yansu-agent/sherpa/tts
	modelDir  string // <root>/vits-melo-tts-zh_en
	model     string // <modelDir>/model.onnx
	lexicon   string // <modelDir>/lexicon.txt
	tokens    string // <modelDir>/tokens.txt
	phoneFst  string // <modelDir>/phone.fst
	dateFst   string // <modelDir>/date.fst
	numberFst string // <modelDir>/number.fst
}

func defaultTtsPaths() ttsPaths {
	dataDir := configDirOrFallback()
	root := filepath.Join(dataDir, "models", "sherpa", "tts")
	md := filepath.Dir(models.Path(dataDir, "sherpa/tts/"+meloTtsDirName+"/model.onnx"))
	return ttsPaths{
		root:      root,
		modelDir:  md,
		model:     filepath.Join(md, "model.onnx"),
		lexicon:   filepath.Join(md, "lexicon.txt"),
		tokens:    filepath.Join(md, "tokens.txt"),
		phoneFst:  filepath.Join(md, "phone.fst"),
		dateFst:   filepath.Join(md, "date.fst"),
		numberFst: filepath.Join(md, "number.fst"),
	}
}

// kokoroPaths describes where the Kokoro English model files live on disk.
type kokoroPaths struct {
	root     string // ~/.yansu-agent/sherpa/tts
	modelDir string // <root>/kokoro-en-v0_19
	model    string // <modelDir>/model.onnx
	voices   string // <modelDir>/voices.bin
	tokens   string // <modelDir>/tokens.txt
	dataDir  string // <modelDir>/espeak-ng-data
}

func defaultKokoroPaths() kokoroPaths {
	dataDir := configDirOrFallback()
	root := filepath.Join(dataDir, "models", "sherpa", "tts")
	md := filepath.Dir(models.Path(dataDir, "sherpa/tts/"+kokoroEnDirName+"/model.onnx"))
	return kokoroPaths{
		root:     root,
		modelDir: md,
		model:    filepath.Join(md, "model.onnx"),
		voices:   filepath.Join(md, "voices.bin"),
		tokens:   filepath.Join(md, "tokens.txt"),
		dataDir:  filepath.Join(md, "espeak-ng-data"),
	}
}

// extractTarBz2 unpacks a .tar.bz2 archive into destRoot, guarding against path
// traversal. The archive is trusted (SHA-256 verified before this call), so we
// extract regular files and directories without an artificial size cap.
func extractTarBz2(archivePath, destRoot string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(bzip2.NewReader(f))
	cleanRoot := filepath.Clean(destRoot)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		target := filepath.Join(destRoot, hdr.Name)
		if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}
