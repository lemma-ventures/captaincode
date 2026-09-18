package captaincode

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
)

// web2.txt.gz is Webster's Second International (public domain), lowercased,
// one alphabetic word per line. Used when the machine has no
// /usr/share/dict/words, so a folder name is classified the same way on
// macOS and on a bare Ubuntu image.
//
//go:embed web2.txt.gz
var web2Gz []byte

func embeddedWords() map[string]bool {
	r, err := gzip.NewReader(bytes.NewReader(web2Gz))
	if err != nil {
		return map[string]bool{}
	}
	defer r.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		w := strings.TrimSpace(sc.Text())
		if w != "" {
			out[w] = true
		}
	}
	return out
}
