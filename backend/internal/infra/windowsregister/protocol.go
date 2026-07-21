package windowsregister

import "encoding/binary"

const maxProtocolStringBytes = 1 << 20

func createEmailValidationFrame(email string) []byte {
	payload := protobufString(1, email)
	if payload == nil {
		return nil
	}
	return grpcWebFrame(payload)
}

func verifyEmailValidationFrame(email, code string) []byte {
	emailField := protobufString(1, email)
	codeField := protobufString(2, code)
	if emailField == nil || codeField == nil {
		return nil
	}
	payload := make([]byte, 0, len(emailField)+len(codeField))
	payload = append(payload, emailField...)
	payload = append(payload, codeField...)
	return grpcWebFrame(payload)
}

func protobufString(field byte, value string) []byte {
	if len(value) > maxProtocolStringBytes || field == 0 || field > 15 {
		return nil
	}
	var encodedLength [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encodedLength[:], uint64(len(value)))
	encoded := make([]byte, 0, 1+n+len(value))
	encoded = append(encoded, field<<3|2)
	encoded = append(encoded, encodedLength[:n]...)
	encoded = append(encoded, value...)
	return encoded
}

func grpcWebFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}
