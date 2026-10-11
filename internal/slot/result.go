// Package slot is the contract between a slot run and its runner: what the runner puts into
// the run's workspace, what the run has to leave behind, and the checks the runner makes
// before it records anything of it.
package slot

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	golusorisschema "github.com/golusoris/golusoris/jsonschema"
	"github.com/tekmora/epitrop/internal/units"
)

// The places of the contract inside a run's workspace.
const (
	InDir       = ".slot/in"
	OutDir      = ".slot/out"
	ResultFile  = OutDir + "/result.json"
	EvidenceDir = OutDir + "/evidence"
)

// The outcomes a run may report.
const (
	OutcomeDone    = "done"
	OutcomeBlocked = "blocked"
	OutcomeFailed  = "failed"
)

const (
	// MaxResultBytes bounds result.json.
	MaxResultBytes = 64 << 10
	// MaxEvidenceBytes bounds one evidence file.
	MaxEvidenceBytes = 8 << 20
)

//go:embed result.schema.json
var resultSchemaJSON []byte

// ResultSchema returns the JSON Schema of result.json.
func ResultSchema() []byte {
	return append([]byte(nil), resultSchemaJSON...)
}

// ErrResult says the run left no result the runner can take. The runner then records the
// run as failed: a result that does not validate proves nothing about the work.
var ErrResult = errors.New("slot: the run's result is not valid")

// Result is what a run leaves in ResultFile.
type Result struct {
	Unit      string     `json:"unit"`
	Outcome   string     `json:"outcome"`
	Head      string     `json:"head,omitempty"`
	Summary   string     `json:"summary"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	Questions []string   `json:"questions,omitempty"`
}

// Evidence names one file under EvidenceDir, with the digest and line count the run claims
// for it.
type Evidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Lines  int    `json:"lines"`
}

// ReadResult reads and checks the result of the run for unit in workspace. Every refusal
// wraps ErrResult.
//
// The document has to be a regular file of at most MaxResultBytes that validates against
// the schema and names this unit. An outcome of done needs the head commit. Each evidence
// entry has to be a regular file inside EvidenceDir, reached without leaving it through a
// symlink or a dot-dot, of at most MaxEvidenceBytes, with the digest and the line count the
// result claims: what the runner records is then what the run wrote.
func ReadResult(workspace string, unit string) (Result, error) {
	result, _, err := readResult(workspace, unit)
	return result, err
}

// readResult is ReadResult, and also returns the bytes that were checked: the runner copies
// those, not what a second read of the file would give.
func readResult(workspace string, unit string) (Result, []byte, error) {
	if !units.IsValidID(unit) {
		return Result{}, nil, fmt.Errorf("%w: invalid unit id %q", ErrResult, unit)
	}
	body, err := readRegular(filepath.Join(workspace, filepath.FromSlash(ResultFile)), MaxResultBytes)
	if err != nil {
		return Result{}, nil, fmt.Errorf("%w: %w", ErrResult, err)
	}
	result, err := decodeResult(body)
	if err != nil {
		return Result{}, nil, err
	}
	if result.Unit != unit {
		return Result{}, nil, fmt.Errorf("%w: it is for unit %q, the run was started for %q", ErrResult, result.Unit, unit)
	}
	if result.Outcome == OutcomeDone && result.Head == "" {
		return Result{}, nil, fmt.Errorf("%w: outcome done needs the head commit", ErrResult)
	}
	if err = checkEvidence(workspace, result.Evidence); err != nil {
		return Result{}, nil, err
	}
	return result, body, nil
}

// decodeResult validates the document against the schema and decodes it.
func decodeResult(body []byte) (Result, error) {
	schema, err := golusorisschema.Compile("result.schema.json", resultSchemaJSON)
	if err != nil {
		return Result{}, fmt.Errorf("slot: compile the result schema: %w", err)
	}
	if err = schema.Validate(body); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrResult, err)
	}
	var result Result
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrResult, err)
	}
	return result, nil
}

// checkEvidence checks every evidence entry against the file it names. The paths are checked
// as text first, so that a path that may not be read is refused before anything is read.
func checkEvidence(workspace string, evidence []Evidence) error {
	seen := map[string]bool{}
	for i := 0; i < len(evidence); i++ {
		if seen[evidence[i].Path] {
			return fmt.Errorf("%w: evidence %q is listed twice", ErrResult, evidence[i].Path)
		}
		seen[evidence[i].Path] = true
		if err := checkEvidencePath(evidence[i].Path); err != nil {
			return fmt.Errorf("%w: evidence %q: %w", ErrResult, evidence[i].Path, err)
		}
	}
	if len(evidence) == 0 {
		return nil
	}
	root, err := filepath.EvalSymlinks(filepath.Join(workspace, filepath.FromSlash(EvidenceDir)))
	if err != nil {
		return fmt.Errorf("%w: evidence directory: %w", ErrResult, err)
	}
	for i := 0; i < len(evidence); i++ {
		if err = checkEvidenceFile(workspace, root, evidence[i]); err != nil {
			return fmt.Errorf("%w: evidence %q: %w", ErrResult, evidence[i].Path, err)
		}
	}
	return nil
}

// checkEvidencePath refuses a path that is not a clean relative path under the evidence
// directory: absolute, with a dot-dot, or somewhere else in the workspace.
func checkEvidencePath(rel string) error {
	slash := filepath.ToSlash(rel)
	if filepath.IsAbs(rel) || strings.HasPrefix(slash, "/") || slash != filepath.ToSlash(filepath.Clean(rel)) {
		return errors.New("the path is not a clean relative path")
	}
	if !strings.HasPrefix(slash, EvidenceDir+"/") {
		return fmt.Errorf("the path is not under %s/", EvidenceDir)
	}
	return nil
}

// checkEvidenceFile checks one entry: where the file is, what it is and what it holds. A
// file that a symlink leads out of the evidence directory is refused.
func checkEvidenceFile(workspace string, root string, entry Evidence) error {
	file := filepath.Join(workspace, filepath.FromSlash(entry.Path))
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return errors.New("the path leaves the evidence directory through a symlink")
	}
	body, err := readRegular(file, MaxEvidenceBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if digest := hex.EncodeToString(sum[:]); digest != entry.SHA256 {
		return fmt.Errorf("the file's sha256 is %s, the result claims %s", digest, entry.SHA256)
	}
	if lines := bytes.Count(body, []byte("\n")); lines != entry.Lines {
		return fmt.Errorf("the file has %d lines, the result claims %d", lines, entry.Lines)
	}
	return nil
}

// readRegular reads a regular file of at most limit bytes. A symlink, a directory or a
// device is refused, and so is a file that was swapped between the look and the open.
func readRegular(path string, limit int) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	// #nosec G304 -- the path is the result or an evidence file inside the run's workspace,
	// checked to be a regular file here and bounded below.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	body, err := readOpened(f, before, limit)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return body, err
}

func readOpened(f *os.File, before os.FileInfo, limit int) ([]byte, error) {
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%s changed while it was opened", f.Name())
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", f.Name(), limit)
	}
	return body, nil
}
