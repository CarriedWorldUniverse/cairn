// Copyright 2015 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"

	"forgejo.org/modules/log"
	"forgejo.org/modules/typesniffer"
)

// Blob represents a Git object.
type Blob struct {
	ID ObjectID

	gotSize bool
	size    int64
	name    string
	repo    *Repository
}

func (b *Blob) newReader() (*bufio.Reader, int64, func(), error) {
	wr, rd, cancel, err := b.repo.CatFileBatch(b.repo.Ctx)
	if err != nil {
		return nil, 0, nil, err
	}

	_, err = wr.Write([]byte(b.ID.String() + "\n"))
	if err != nil {
		cancel()
		return nil, 0, nil, err
	}
	_, _, size, err := ReadBatchLine(rd)
	if err != nil {
		cancel()
		return nil, 0, nil, err
	}
	b.gotSize = true
	b.size = size
	return rd, size, cancel, err
}

// Size returns the uncompressed size of the blob
func (b *Blob) Size() int64 {
	if b.gotSize {
		return b.size
	}

	wr, rd, cancel, err := b.repo.CatFileBatchCheck(b.repo.Ctx)
	if err != nil {
		log.Debug("error whilst reading size for %s in %s. Error: %v", b.ID.String(), b.repo.Path, err)
		return 0
	}
	defer cancel()
	_, err = wr.Write([]byte(b.ID.String() + "\n"))
	if err != nil {
		log.Debug("error whilst reading size for %s in %s. Error: %v", b.ID.String(), b.repo.Path, err)
		return 0
	}
	_, _, b.size, err = ReadBatchLine(rd)
	if err != nil {
		log.Debug("error whilst reading size for %s in %s. Error: %v", b.ID.String(), b.repo.Path, err)
		return 0
	}

	b.gotSize = true

	return b.size
}

// DataAsync gets a ReadCloser for the contents of a blob without reading it all.
// Calling the Close function on the result will discard all unread output.
func (b *Blob) DataAsync() (io.ReadCloser, error) {
	rd, size, cancel, err := b.newReader()
	if err != nil {
		return nil, err
	}

	if size < 4096 {
		bs, err := io.ReadAll(io.LimitReader(rd, size))
		defer cancel()
		if err != nil {
			return nil, err
		}
		_, err = rd.Discard(1)
		return io.NopCloser(bytes.NewReader(bs)), err
	}

	return &blobReader{
		rd:     rd,
		n:      size,
		cancel: cancel,
	}, nil
}

type blobReader struct {
	rd                *bufio.Reader
	n                 int64 // number of bytes to read
	additionalDiscard int64 // additional number of bytes to discard
	cancel            func()
}

func (b *blobReader) Read(p []byte) (n int, err error) {
	if b.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.n {
		p = p[0:b.n]
	}
	n, err = b.rd.Read(p)
	b.n -= int64(n)
	return n, err
}

// Close implements io.Closer
func (b *blobReader) Close() error {
	if b.rd == nil {
		return nil
	}

	defer b.cancel()

	// discard the unread bytes, the truncated bytes and the trailing newline
	if err := DiscardFull(b.rd, b.n+b.additionalDiscard+1); err != nil {
		return err
	}

	b.rd = nil

	return nil
}

// Name returns name of the tree entry this blob object was created from (or empty string)
func (b *Blob) Name() string {
	return b.name
}

// NewTruncatedReader return a blob-reader which silently truncates when the limit is reached (io.EOF will be returned)
func (b *Blob) NewTruncatedReader(limit int64) (rc io.ReadCloser, fullSize int64, err error) {
	r, fullSize, cancel, err := b.newReader()
	if err != nil {
		return nil, fullSize, err
	}

	limit = min(limit, fullSize)
	return &blobReader{
		rd:                r,
		n:                 limit,
		additionalDiscard: fullSize - limit,
		cancel:            cancel,
	}, fullSize, nil
}

// GetBlobContent Gets the truncated content of the blob as raw text
func (b *Blob) GetBlobContent(limit int64) (string, error) {
	if limit <= 0 {
		return "", nil
	}
	rc, fullSize, err := b.NewTruncatedReader(limit)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	buf := make([]byte, min(fullSize, limit))
	_, err = io.ReadFull(rc, buf)
	return string(buf), err
}

// GetBlobContentBase64 Reads the content of the blob with a base64 encode and returns the encoded string
func (b *Blob) GetBlobContentBase64() (string, error) {
	dataRc, err := b.DataAsync()
	if err != nil {
		return "", err
	}
	defer dataRc.Close()

	pr, pw := io.Pipe()
	encoder := base64.NewEncoder(base64.StdEncoding, pw)

	go func() {
		_, err := io.Copy(encoder, dataRc)
		_ = encoder.Close()

		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
	}()

	out, err := io.ReadAll(pr)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// GuessContentType guesses the content type of the blob.
func (b *Blob) GuessContentType() (typesniffer.SniffedType, error) {
	r, err := b.DataAsync()
	if err != nil {
		return typesniffer.SniffedType{}, err
	}
	defer r.Close()

	return typesniffer.DetectContentTypeFromReader(r)
}

// GetBlob finds the blob object in the repository.
func (repo *Repository) GetBlob(idStr string) (*Blob, error) {
	id, err := NewIDFromString(idStr)
	if err != nil {
		return nil, err
	}
	if id.IsZero() {
		return nil, ErrNotExist{id.String(), ""}
	}
	return &Blob{
		ID:   id,
		repo: repo,
	}, nil
}
