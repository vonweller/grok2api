package windowsregister

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestCreateEmailValidationFrame(t *testing.T) {
	got := createEmailValidationFrame("a@b")
	want := []byte{0, 0, 0, 0, 5, 0x0a, 0x03, 'a', '@', 'b'}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
}

func TestVerifyEmailValidationFrame(t *testing.T) {
	got := verifyEmailValidationFrame("a@b", "123456")
	if len(got) < 5 || got[0] != 0 || binary.BigEndian.Uint32(got[1:5]) != uint32(len(got)-5) {
		t.Fatalf("invalid grpc-web frame: %x", got)
	}
	wantPayload := []byte{0x0a, 0x03, 'a', '@', 'b', 0x12, 0x06, '1', '2', '3', '4', '5', '6'}
	if !bytes.Equal(got[5:], wantPayload) {
		t.Fatalf("payload = %x, want %x", got[5:], wantPayload)
	}
}

func TestValidationFrameRejectsOversizedStrings(t *testing.T) {
	oversized := strings.Repeat("x", maxProtocolStringBytes+1)
	if got := createEmailValidationFrame(oversized); got != nil {
		t.Fatalf("oversized frame length = %d", len(got))
	}
	if got := verifyEmailValidationFrame("a@b", oversized); got != nil {
		t.Fatalf("oversized verify frame length = %d", len(got))
	}
}
