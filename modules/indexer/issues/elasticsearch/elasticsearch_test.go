// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package elasticsearch

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"forgejo.org/modules/indexer/issues/internal/tests"
)

func TestElasticsearchIndexer(t *testing.T) {
	url := os.Getenv("TEST_ELASTICSEARCH_URL")
	if url == "" {
		t.Skip("TEST_ELASTICSEARCH_URL not set")
		return
	}

	ok := false
	for i := 0; i < 60; i++ {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			ok = true
			break
		}
		t.Logf("Waiting for elasticsearch to be up: %v", err)
		time.Sleep(time.Second)
	}
	if !ok {
		t.Fatalf("Failed to wait for elasticsearch to be up")
		return
	}

	indexer := NewIndexer(url, fmt.Sprintf("test_elasticsearch_indexer_%d", time.Now().Unix()))
	defer indexer.Close()

	tests.TestIndexer(t, indexer)
}
