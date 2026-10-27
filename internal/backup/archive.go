package backup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Part names inside an archive.
const (
	PartManifest = "manifest.json"
	PartObjects  = "objects.json"
	PartDB       = "db.dump"
)

// FormatVersion is the archive layout version.
const FormatVersion = 1

const (
	magic     = "HBK1"
	chunkSize = 1 << 20
	kindData  = 0
	kindEnd   = 1
	// maxFrame bounds a frame a reader will allocate, so a corrupt length
	// cannot exhaust memory.
	maxFrame = chunkSize + 64
	maxName  = 64
)

// Errors a reader can return.
var (
	// ErrKeyMismatch: the archive was written under a different key.
	ErrKeyMismatch = errors.New("backup: archive was encrypted under a different key")
	// ErrCorrupt: the archive is truncated, tampered with or not an archive.
	ErrCorrupt = errors.New("backup: archive is corrupt")
)

// PartInfo is one part's plaintext size and SHA-256 in the manifest.
type PartInfo struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Counts summarise what an archive holds.
type Counts struct {
	Objects     int   `json:"objects"`
	ObjectBytes int64 `json:"objectBytes"`
	DBBytes     int64 `json:"dbBytes"`
}

// Manifest describes an archive. It is the first part, so reading it does
// not read the dump.
type Manifest struct {
	Format        int                 `json:"format"`
	HelloVersion  string              `json:"helloVersion"`
	SchemaVersion int64               `json:"schemaVersion"`
	CreatedAt     time.Time           `json:"createdAt"`
	RunID         int64               `json:"runId"`
	KeyID         string              `json:"keyFingerprint"`
	Counts        Counts              `json:"counts"`
	Parts         map[string]PartInfo `json:"parts"`
}

// Part is one named stream to write.
type Part struct {
	Name string
	Data io.Reader
}

// Hash of a stream: its length and SHA-256.
func hashOf(r io.Reader) (PartInfo, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	return PartInfo{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

// HashBytes returns the part info of b.
func HashBytes(b []byte) PartInfo {
	s := sha256.Sum256(b)
	return PartInfo{Size: int64(len(b)), SHA256: hex.EncodeToString(s[:])}
}

// Write encrypts parts to w. Each part is split into 1 MiB chunks, every
// chunk sealed with AES-256-GCM under a random 96-bit nonce; the additional
// data binds the part name, chunk index and chunk kind, so chunks cannot be
// reordered, moved between parts or cut short without detection.
//
//	archive := "HBK1" fingerprint(8) part*
//	part    := nameLen(1) name frame*
//	frame   := kind(1) length(4) nonce(12) ciphertext   (kind 1 ends the part)
func Write(w io.Writer, k Key, parts []Part) error {
	if k.aead == nil {
		return ErrKey
	}
	if _, err := io.WriteString(w, magic); err != nil {
		return err
	}
	if _, err := w.Write(k.fp[:]); err != nil {
		return err
	}
	for _, p := range parts {
		if len(p.Name) == 0 || len(p.Name) > maxName {
			return fmt.Errorf("backup: bad part name %q", p.Name)
		}
		nameFrame := append([]byte{byte(len(p.Name))}, p.Name...) //nolint:gosec // G115: the length was checked above (1 to 64)
		if _, err := w.Write(nameFrame); err != nil {
			return err
		}
		buf := make([]byte, chunkSize)
		var idx uint64
		for {
			n, err := io.ReadFull(p.Data, buf)
			if n > 0 {
				if werr := writeFrame(w, k, p.Name, idx, kindData, buf[:n]); werr != nil {
					return werr
				}
				idx++
			}
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			if err != nil {
				return err
			}
		}
		if err := writeFrame(w, k, p.Name, idx, kindEnd, nil); err != nil {
			return err
		}
	}
	return nil
}

func aad(name string, idx uint64, kind byte) []byte {
	b := make([]byte, 0, len(name)+10)
	b = append(b, name...)
	b = binary.BigEndian.AppendUint64(b, idx)
	return append(b, kind)
}

func writeFrame(w io.Writer, k Key, name string, idx uint64, kind byte, plain []byte) error {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := k.aead.Seal(nil, nonce, plain, aad(name, idx, kind))
	hdr := make([]byte, 5, 5+len(nonce))
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(nonce)+len(sealed))) //nolint:gosec // G115: a frame is at most chunkSize plus overhead
	hdr = append(hdr, nonce...)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(sealed)
	return err
}

// Reader reads an archive part by part.
type Reader struct {
	r   io.Reader
	k   Key
	cur *partReader
}

// Open reads the header and refuses an archive written under another key.
func Open(r io.Reader, k Key) (*Reader, error) {
	if k.aead == nil {
		return nil, ErrKey
	}
	hdr := make([]byte, len(magic)+8)
	if _, err := io.ReadFull(r, hdr); err != nil || string(hdr[:len(magic)]) != magic {
		return nil, ErrCorrupt
	}
	if !bytes.Equal(hdr[len(magic):], k.fp[:]) {
		return nil, ErrKeyMismatch
	}
	return &Reader{r: r, k: k}, nil
}

// Next returns the next part's name and its decrypted body, io.EOF after the
// last. The previous body is drained first. Reading a body to the end
// returns an error when any chunk fails authentication or the part is cut
// short.
func (r *Reader) Next() (string, io.Reader, error) {
	if r.cur != nil {
		if _, err := io.Copy(io.Discard, r.cur); err != nil {
			return "", nil, err
		}
	}
	var l [1]byte
	if _, err := io.ReadFull(r.r, l[:]); err != nil {
		if err == io.EOF {
			return "", nil, io.EOF
		}
		return "", nil, ErrCorrupt
	}
	name := make([]byte, l[0])
	if _, err := io.ReadFull(r.r, name); err != nil || len(name) == 0 {
		return "", nil, ErrCorrupt
	}
	r.cur = &partReader{r: r.r, k: r.k, name: string(name)}
	return string(name), r.cur, nil
}

type partReader struct {
	r    io.Reader
	k    Key
	name string
	idx  uint64
	buf  []byte
	done bool
}

func (p *partReader) Read(b []byte) (int, error) {
	for len(p.buf) == 0 {
		if p.done {
			return 0, io.EOF
		}
		var hdr [5]byte
		if _, err := io.ReadFull(p.r, hdr[:]); err != nil {
			return 0, ErrCorrupt
		}
		kind := hdr[0]
		n := binary.BigEndian.Uint32(hdr[1:])
		ns := p.k.aead.NonceSize()
		if kind > kindEnd || int(n) < ns+p.k.aead.Overhead() || n > maxFrame {
			return 0, ErrCorrupt
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(p.r, frame); err != nil {
			return 0, ErrCorrupt
		}
		plain, err := p.k.aead.Open(nil, frame[:ns], frame[ns:], aad(p.name, p.idx, kind))
		if err != nil {
			return 0, ErrCorrupt
		}
		p.idx++
		if kind == kindEnd {
			p.done = true
			continue
		}
		p.buf = plain
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

// Build lays out an archive: the manifest first (so it can be read alone),
// then objects.json and the dump. Part hashes are filled in from the
// supplied bytes and files; open returns a fresh reader of the dump.
func Build(w io.Writer, k Key, m Manifest, objects []byte, dumpInfo PartInfo, dump io.Reader) error {
	m.Format = FormatVersion
	m.KeyID = k.Fingerprint()
	m.Counts.DBBytes = dumpInfo.Size
	m.Parts = map[string]PartInfo{PartObjects: HashBytes(objects), PartDB: dumpInfo}
	mb, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return Write(w, k, []Part{
		{PartManifest, bytes.NewReader(mb)},
		{PartObjects, bytes.NewReader(objects)},
		{PartDB, dump},
	})
}

// HashReader hashes r to its end.
func HashReader(r io.Reader) (PartInfo, error) { return hashOf(r) }

// ReadManifest reads only the first part of an archive.
func ReadManifest(r io.Reader, k Key) (Manifest, error) {
	ar, err := Open(r, k)
	if err != nil {
		return Manifest{}, err
	}
	name, body, err := ar.Next()
	if err != nil || name != PartManifest {
		return Manifest{}, ErrCorrupt
	}
	b, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, ErrCorrupt
	}
	if m.KeyID != k.Fingerprint() {
		return Manifest{}, ErrKeyMismatch
	}
	return m, nil
}
