package windowsregister_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
)

func TestFileResultStoreConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "accounts.txt")
	store := windowsregister.NewFileResultStore(path)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Append(windowsregister.Record{
				Email:    fmt.Sprintf("u%d@x.test", i),
				Password: "password",
				SSO:      fmt.Sprintf("sso-%d", i),
			}); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	records, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 20 {
		t.Fatalf("records = %d", len(records))
	}
}

func TestFileResultStoreRejectsFieldInjection(t *testing.T) {
	store := windowsregister.NewFileResultStore(filepath.Join(t.TempDir(), "accounts.txt"))
	for _, record := range []windowsregister.Record{
		{Email: "u:admin@x.test", Password: "password", SSO: "sso"},
		{Email: "u@x.test\nother", Password: "password", SSO: "sso"},
		{Email: "u@x.test", Password: "pass:word", SSO: "sso"},
		{Email: "u@x.test", Password: "password\nother", SSO: "sso"},
		{Email: "u@x.test", Password: "password", SSO: "sso\nother"},
	} {
		if err := store.Append(record); !errors.Is(err, windowsregister.ErrInvalidRecord) {
			t.Fatalf("record %#v error = %v", record, err)
		}
	}
	records, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("invalid records were written: %#v", records)
	}
}
