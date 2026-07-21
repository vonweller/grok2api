package windowsregister

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrInvalidRecord = errors.New("registration record invalid")
	ErrResultStore   = errors.New("registration result store failed")
)

type ResultStore interface {
	Append(Record) error
	Read() ([]Record, error)
}

type FileResultStore struct {
	path string
	mu   sync.Mutex
}

func NewFileResultStore(path string) *FileResultStore {
	return &FileResultStore{path: strings.TrimSpace(path)}
}

func (s *FileResultStore) Append(record Record) error {
	record, err := validatedRecord(record)
	if err != nil {
		return err
	}
	if s.path == "" {
		return ErrResultStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return ErrResultStore
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrResultStore
	}
	writer := bufio.NewWriter(file)
	_, writeErr := writer.WriteString(record.Email + ":" + record.Password + ":" + record.SSO + "\n")
	if writeErr == nil {
		writeErr = writer.Flush()
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return ErrResultStore
	}
	return nil
}

func (s *FileResultStore) Read() ([]Record, error) {
	if s.path == "" {
		return nil, ErrResultStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := ReadAccountsFile(s.path)
	if err != nil {
		return nil, ErrResultStore
	}
	return records, nil
}

func validatedRecord(record Record) (Record, error) {
	record.Email = strings.TrimSpace(record.Email)
	record.Password = strings.TrimSpace(record.Password)
	record.SSO = strings.TrimSpace(record.SSO)
	if !strings.Contains(record.Email, "@") || record.Password == "" || record.SSO == "" ||
		strings.ContainsAny(record.Email, ":\r\n") || strings.ContainsAny(record.Password, ":\r\n") || strings.ContainsAny(record.SSO, "\r\n") {
		return Record{}, ErrInvalidRecord
	}
	return record, nil
}
