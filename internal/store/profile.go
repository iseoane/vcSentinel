package store

import "errors"

const (
	subdirProfiles    = "profiles"
	ProfileUnverified = "unverified"
)

// Profile records whether a configured profile has been verified by its agent.
type Profile struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Event         string `json:"event,omitempty"`
	ExpectedModel string `json:"expected_model,omitempty"`
	ActualModel   string `json:"actual_model,omitempty"`
}

// GuardarPerfil persists p in profiles/<name>.json.
func (s *Store) GuardarPerfil(p *Profile) error {
	if p.Name == "" {
		return errors.New("store: profile without name")
	}
	return s.guardarJSON(subdirProfiles, p.Name, p)
}

// LeerPerfil returns the profile named name, or nil when it was not recorded.
func (s *Store) LeerPerfil(name string) (*Profile, error) {
	var p Profile
	ok, err := s.leerJSON(subdirProfiles, name, &p)
	if err != nil || !ok {
		return nil, err
	}
	return &p, nil
}
