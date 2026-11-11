package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// HashObject returns the object name git gives data stored as type t
// ("blob", "tree", "commit" or "tag"), without storing anything.
func HashObject(t string, data []byte) (string, error) {
	ot, err := plumbing.ParseObjectType(t)
	if err != nil || ot == plumbing.AnyObject || ot == plumbing.OFSDeltaObject || ot == plumbing.REFDeltaObject {
		return "", fmt.Errorf("invalid object type %q", t)
	}
	return plumbing.ComputeHash(ot, data).String(), nil
}

// WriteObject stores data as an object of type t and returns its name.
func (e *Engine) WriteObject(t string, data []byte) (string, error) {
	ot, err := plumbing.ParseObjectType(t)
	if err != nil || ot == plumbing.AnyObject || ot == plumbing.OFSDeltaObject || ot == plumbing.REFDeltaObject {
		return "", fmt.Errorf("invalid object type %q", t)
	}
	if ot == plumbing.BlobObject {
		return e.WriteBlob(data)
	}
	obj := e.git.Storer.NewEncodedObject()
	obj.SetType(ot)
	w, err := obj.Writer()
	if err != nil {
		return "", fmt.Errorf("change.WriteObject: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("change.WriteObject: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("change.WriteObject: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", fmt.Errorf("change.WriteObject: %w", err)
	}
	return h.String(), nil
}
