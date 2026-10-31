// Package protocol defines the versioned wire protocol spoken between the
// recom client and the recom remote helper over an SSH session.
//
// The helper writes Magic to its stdout before anything else. The client
// discards any bytes before Magic (they come from noisy shell startup files)
// and from then on both directions carry length-prefixed frames:
//
//	+--------+----------------+-----------------+
//	| type:1 | length:4 (BE)  | payload:length  |
//	+--------+----------------+-----------------+
//
// Control frames carry JSON payloads; data frames carry raw bytes. Remote
// program output is only ever carried inside data frames, so it can never be
// mistaken for control metadata.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Version is the protocol version. Client and helper must agree exactly.
const Version = 1

// Magic is written by the helper before the first frame.
const Magic = "\x00\x1bRECOM-HELPER/1\x00"

// MaxFrame bounds a single frame payload.
const MaxFrame = 8 << 20

// MaxNoise bounds how many bytes of shell-startup noise the client tolerates
// before the magic preamble.
const MaxNoise = 256 << 10

// Frame types.
const (
	FHello    byte = 'H' // client -> helper: Hello
	FAck      byte = 'A' // helper -> client: Ack
	FRequest  byte = 'Q' // client -> helper: Request
	FResponse byte = 'R' // helper -> client: Response (ends a request)
	FEvent    byte = 'E' // helper -> client: Event (progress/info)
	FStdin    byte = 'i' // client -> helper: stdin bytes
	FStdinEOF byte = 'I' // client -> helper: stdin closed
	FStdout   byte = 'o' // helper -> client: stdout bytes
	FStderr   byte = 'e' // helper -> client: stderr bytes
	FSignal   byte = 'S' // client -> helper: Signal
	FResize   byte = 'W' // client -> helper: Resize
)

// Frame is one decoded frame.
type Frame struct {
	Type    byte
	Payload []byte
}

// Writer writes frames. It is safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter returns a frame writer.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write writes a raw frame.
func (fw *Writer) Write(t byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("protocol: frame too large (%d bytes)", len(payload))
	}
	var hdr [5]byte
	hdr[0] = t
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if _, err := fw.w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := fw.w.Write(payload); err != nil {
			return err
		}
	}
	if f, ok := fw.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// WriteJSON writes a frame with a JSON payload.
func (fw *Writer) WriteJSON(t byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return fw.Write(t, b)
}

// Reader reads frames.
type Reader struct {
	r *bufio.Reader
}

// NewReader returns a frame reader.
func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReaderSize(r, 64<<10)} }

// ReadFrame reads the next frame.
func (fr *Reader) ReadFrame() (Frame, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(fr.r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return Frame{}, fmt.Errorf("protocol: frame too large (%d bytes); stream corrupted", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(fr.r, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return Frame{Type: hdr[0], Payload: p}, nil
}

// SkipToMagic consumes bytes until Magic has been read. It returns the noise
// that preceded it. It fails if more than MaxNoise bytes arrive first.
func (fr *Reader) SkipToMagic() ([]byte, error) {
	var buf []byte
	m := []byte(Magic)
	for {
		b, err := fr.r.ReadByte()
		if err != nil {
			return buf, err
		}
		buf = append(buf, b)
		if len(buf) >= len(m) && bytes.Equal(buf[len(buf)-len(m):], m) {
			return buf[:len(buf)-len(m)], nil
		}
		if len(buf) > MaxNoise {
			return buf, errors.New("protocol: helper preamble not found (remote shell output too noisy)")
		}
	}
}

// Decode unmarshals a JSON frame payload, failing closed on garbage.
func Decode(f Frame, v any) error {
	dec := json.NewDecoder(bytes.NewReader(f.Payload))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("protocol: malformed %q frame: %w", f.Type, err)
	}
	return nil
}
