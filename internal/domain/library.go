package domain

import (
	"errors"
	"regexp"
)

// LibrarySnapshot records the client's selected declaration at launch. It does
// not certify native configuration loading, permissions or tool invocation.
type LibrarySnapshot struct {
	ProfileID         string `json:"profile_id"`
	ProfileRevision   uint64 `json:"profile_revision"`
	ResolutionDigest  string `json:"resolution_digest"`
	ApplicationID     string `json:"application_id,omitempty"`
	ApplicationDigest string `json:"application_digest,omitempty"`
	Source            string `json:"source"`
	Loaded            string `json:"loaded"`
}

var libraryID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var libraryDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (v LibrarySnapshot) Validate() error {
	if !libraryID.MatchString(v.ProfileID) || v.ProfileRevision == 0 || !libraryDigest.MatchString(v.ResolutionDigest) ||
		v.Source != "client-declaration" || v.Loaded != "unknown" {
		return errors.New("library snapshot requires bounded profile, exact digest and unknown client-declared load state")
	}
	if (v.ApplicationID == "") != (v.ApplicationDigest == "") ||
		(v.ApplicationID != "" && (!libraryID.MatchString(v.ApplicationID) || !libraryDigest.MatchString(v.ApplicationDigest))) {
		return errors.New("library application requires both bounded ID and exact digest")
	}
	return nil
}
