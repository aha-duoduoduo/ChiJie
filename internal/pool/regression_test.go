package pool

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStopSubscriptionUpdaterCancelsPendingFetch(t *testing.T) {
	var calls atomic.Int32
	pending := make(chan struct{})
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			close(pending)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("proxies:\n  - name: echo\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\n"))
	}))
	defer subscription.Close()
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	config := fmt.Sprintf("node_pools:\n  auto:\n    source: subscription\n    url: %q\n    allow_private_host: true\n    update_interval: 20ms\n", subscription.URL)
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	if err := m.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	m.StartSubscriptionUpdater()
	select {
	case <-pending:
	case <-time.After(2 * time.Second):
		t.Fatal("updater did not start")
	}
	stopped := make(chan struct{})
	go func() { m.StopSubscriptionUpdater(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("updater did not cancel pending HTTP request")
	}
}

func TestRegressionPoolSnapshotRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if err := os.WriteFile(path, []byte("node_pools:\n  audit:\n    source: static\n    nodes:\n      - name: audit\n        type: direct\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	if err := m.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = m.SetNodeEnabled("audit", "audit", i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_, _ = json.Marshal(m.GetPoolStatus())
		}
	}()
	wg.Wait()
}
