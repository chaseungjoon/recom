package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	buf.WriteString("Welcome to Ubuntu!\nmotd noise\n") // shell startup noise
	buf.WriteString(Magic)
	if err := w.WriteJSON(FAck, Ack{Protocol: Version, HelperVersion: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(FStdout, []byte("raw \x00 bytes")); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(FStdinEOF, nil); err != nil {
		t.Fatal(err)
	}
	r := NewReader(&buf)
	noise, err := r.SkipToMagic()
	if err != nil || !strings.Contains(string(noise), "motd noise") {
		t.Fatalf("noise %q %v", noise, err)
	}
	f, err := r.ReadFrame()
	var ack Ack
	if err != nil || f.Type != FAck || Decode(f, &ack) != nil || ack.HelperVersion != "x" {
		t.Fatalf("ack: %v %+v", err, ack)
	}
	f, _ = r.ReadFrame()
	if f.Type != FStdout || string(f.Payload) != "raw \x00 bytes" {
		t.Fatalf("data frame %+v", f)
	}
	f, _ = r.ReadFrame()
	if f.Type != FStdinEOF || len(f.Payload) != 0 {
		t.Fatalf("eof frame %+v", f)
	}
}

func TestOversizeAndMalformedFrames(t *testing.T) {
	var hdr [5]byte
	hdr[0] = FRequest
	binary.BigEndian.PutUint32(hdr[1:], MaxFrame+1)
	if _, err := NewReader(bytes.NewReader(hdr[:])).ReadFrame(); err == nil {
		t.Fatal("oversize frame accepted")
	}
	if err := NewWriter(&bytes.Buffer{}).Write(FStdout, make([]byte, MaxFrame+1)); err == nil {
		t.Fatal("oversize write accepted")
	}
	if Decode(Frame{Type: FRequest, Payload: []byte("{not json")}, &Request{}) == nil {
		t.Fatal("malformed JSON accepted")
	}
	// Truncated payload.
	binary.BigEndian.PutUint32(hdr[1:], 10)
	if _, err := NewReader(bytes.NewReader(append(hdr[:], 'a'))).ReadFrame(); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestSkipToMagicLimit(t *testing.T) {
	noisy := strings.Repeat("x", MaxNoise+10)
	if _, err := NewReader(strings.NewReader(noisy)).SkipToMagic(); err == nil {
		t.Fatal("unbounded noise accepted")
	}
}
