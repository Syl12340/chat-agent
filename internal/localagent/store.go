package localagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"chat-agent/internal/storage"
)

type ReceiptStore struct {
	mu       sync.Mutex
	path     string
	receipts map[string]Receipt
}

func OpenReceiptStore(path string) (*ReceiptStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("local agent state path must be absolute")
	}
	s := &ReceiptStore{path: path, receipts: map[string]Receipt{}}
	raw, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(raw, &s.receipts) != nil {
			return nil, errors.New("local agent receipt store is damaged")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *ReceiptStore) Get(id string) (Receipt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[id]
	return receipt, ok
}

func (s *ReceiptStore) Put(receipt Receipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.receipts[receipt.OperationID]; ok && prior.RequestSHA256 != receipt.RequestSHA256 {
		return errors.New("operation receipt conflict")
	}
	s.receipts[receipt.OperationID] = receipt
	if len(s.receipts) > 2048 {
		var oldestID string
		for id, value := range s.receipts {
			if oldestID == "" || value.CompletedAt.Before(s.receipts[oldestID].CompletedAt) {
				oldestID = id
			}
		}
		delete(s.receipts, oldestID)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(s.receipts)
	if err != nil {
		return err
	}
	return storage.Replace(s.path, raw)
}
