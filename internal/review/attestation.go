package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const attestationMarker = "<!-- vas-sentinel-attestation:"
const attestationSchemaVersion = "v1"

var (
	// ErrAttestationAbsent means that a PR body does not contain a Sentinel
	// attestation. Callers must distinguish this from a malformed attestation:
	// the former has not been authored, while the latter must not be trusted.
	ErrAttestationAbsent = errors.New("vas-sentinel attestation is absent")
	// ErrInvalidAttestation means that the marker exists but its JSON payload is
	// not a valid Attestation document.
	ErrInvalidAttestation = errors.New("vas-sentinel attestation is invalid")
	// ErrUnsupportedAttestationSchema means the reader found a schema it does
	// not understand and therefore cannot safely interpret.
	ErrUnsupportedAttestationSchema = errors.New("vas-sentinel attestation schema is unsupported")
	// ErrMultipleAttestations prevents a body from making the reader silently
	// choose between competing machine claims.
	ErrMultipleAttestations = errors.New("multiple vas-sentinel attestations")
)

// Attestation is the machine-readable summary authored by pr review. Piece 5
// will make pr create consume it. Its schema version lives in the HTML marker,
// not in this payload, so a reader can reject an unknown shape before decoding it.
type Attestation struct {
	HeadSHA string            `json:"head_sha"`
	Branch  string            `json:"branch"`
	Verdict string            `json:"verdict"`
	Steps   []AttestationStep `json:"steps"`
}

// AttestationStep records the observed outcome of one fixed Pipeline step.
type AttestationStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

// RenderAttestation encodes attestation as the one-line v1 marker that is
// inserted immediately before the Pipeline section.
func RenderAttestation(attestation Attestation) (string, error) {
	payload, err := json.Marshal(attestation)
	if err != nil {
		return "", fmt.Errorf("marshal attestation: %w", err)
	}
	return attestationMarker + attestationSchemaVersion + " " + string(payload) + " -->", nil
}

// ParseAttestation reads exactly one Sentinel machine attestation from body.
// It rejects multiple markers rather than taking the first because selecting a
// machine claim from conflicting claims would make pr create non-deterministic.
func ParseAttestation(body string) (Attestation, error) {
	markers, err := findAttestationMarkers(body)
	if err != nil {
		return Attestation{}, err
	}
	switch len(markers) {
	case 0:
		return Attestation{}, ErrAttestationAbsent
	case 1:
	default:
		return Attestation{}, ErrMultipleAttestations
	}

	marker := markers[0]
	if marker.version != attestationSchemaVersion {
		return Attestation{}, fmt.Errorf("%w: %s", ErrUnsupportedAttestationSchema, marker.version)
	}
	var attestation Attestation
	if err := json.Unmarshal([]byte(marker.payload), &attestation); err != nil {
		return Attestation{}, fmt.Errorf("%w: %v", ErrInvalidAttestation, err)
	}
	return attestation, nil
}

type attestationMarkerValue struct {
	version string
	payload string
}

func findAttestationMarkers(body string) ([]attestationMarkerValue, error) {
	var markers []attestationMarkerValue
	for offset := 0; ; {
		start := strings.Index(body[offset:], attestationMarker)
		if start < 0 {
			return markers, nil
		}
		start += offset
		end := strings.Index(body[start:], " -->")
		if end < 0 {
			return nil, fmt.Errorf("%w: unterminated marker", ErrInvalidAttestation)
		}
		end += start
		content := strings.TrimSpace(body[start+len(attestationMarker) : end])
		separator := strings.IndexAny(content, " \t\r\n")
		if separator <= 0 {
			return nil, fmt.Errorf("%w: missing JSON payload", ErrInvalidAttestation)
		}
		markers = append(markers, attestationMarkerValue{
			version: content[:separator],
			payload: strings.TrimSpace(content[separator:]),
		})
		offset = end + len(" -->")
	}
}
