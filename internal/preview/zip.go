package preview

import (
	"archive/zip"
	"bytes"
	"io"
)

// zipFiles opens a zip file's entries by name: the OOXML and OpenDocument
// files are zips of XML parts.
func zipFiles(data []byte) (map[string]*zip.File, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		files[f.Name] = f
	}
	return files, nil
}

// zipRead reads an entry of a zip, nil when the zip has none of that name
// or cannot give it.
func zipRead(files map[string]*zip.File, name string) []byte {
	f, ok := files[name]
	if !ok {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil
	}
	return data
}
