package ceremony

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaterializeExport recreates finalized keys from the authenticated local CAS,
// including after S3 restore. VerifyExport performs the separate MPC replay.
func (s *Store) MaterializeExport(out string) error {
	cp, _, err := s.checkpoint()
	if err != nil {
		return err
	}
	if cp.Lifecycle.Phase != "finalized" || cp.Lifecycle.FinalExport == nil {
		return fmt.Errorf("ceremony has no finalized export")
	}
	if _, err = os.Stat(out); !os.IsNotExist(err) {
		return fmt.Errorf("export destination must be new")
	}
	ref := *cp.Lifecycle.FinalExport
	if err = Check(s.Object(ref), ref); err != nil {
		return err
	}
	var exported ExportManifest
	if err = ReadJSON(s.Object(ref), &exported); err != nil {
		return err
	}
	if exported.ManifestSHA256 != s.ManifestArtifact.SHA256 {
		return fmt.Errorf("export manifest mismatch")
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), ".export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	copyArtifact := func(name string, a Artifact) error {
		if err := Check(s.Object(a), a); err != nil {
			return err
		}
		return Atomic(filepath.Join(tmp, name), func(w io.Writer) error {
			f, err := os.Open(s.Object(a))
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(w, f)
			return err
		})
	}
	for _, c := range exported.Circuits {
		if !safeID.MatchString(c.Circuit.Spec.ID) {
			return fmt.Errorf("invalid exported circuit ID")
		}
		if err = copyArtifact(c.Circuit.Spec.ID+".pk", c.PK); err != nil {
			return err
		}
		if err = copyArtifact(c.Circuit.Spec.ID+".vk", c.VK); err != nil {
			return err
		}
	}
	if err = copyArtifact("export.json", ref); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
