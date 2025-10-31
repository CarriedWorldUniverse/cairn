// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package stats

import (
	"context"
	"errors"
	"sync"
	"testing"

	"forgejo.org/modules/optional"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueueAndFlush(t *testing.T) {
	var mu sync.Mutex
	callValues := []int64{}
	RegisterRecalc(-99, func(ctx context.Context, i int64, _ optional.Option[timeutil.TimeStamp]) error {
		mu.Lock()
		defer mu.Unlock()
		callValues = append(callValues, i)
		return nil
	})

	err := safePush(recalcRequest{
		RecalcType: -99,
		ObjectID:   1,
	})
	require.NoError(t, err)

	require.NoError(t, Flush(t.Context()))
	func() {
		mu.Lock()
		defer mu.Unlock()
		assert.Len(t, callValues, 1)
		assert.EqualValues(t, 1, callValues[0])
	}()
}

func TestQueueUnique(t *testing.T) {
	var mu sync.Mutex
	callValues := []int64{}
	RegisterRecalc(-100, func(ctx context.Context, i int64, _ optional.Option[timeutil.TimeStamp]) error {
		mu.Lock()
		defer mu.Unlock()
		callValues = append(callValues, i)
		return nil
	})

	// Queue object with the same value multiple times... this test works OK with just 3 items, but with the queue
	// processing happening in tha background it's possible that multiple invocations of the registered function can
	// happen.  So we'll test this by queuing a large number and ensuring that recalcs occured less -- usually much
	// less, like once or twice.
	for range 300 {
		err := safePush(recalcRequest{
			RecalcType: -100,
			ObjectID:   1,
		})
		require.NoError(t, err)
	}

	require.NoError(t, Flush(t.Context()))
	func() {
		mu.Lock()
		defer mu.Unlock()
		assert.Less(t, len(callValues), 300)
		assert.EqualValues(t, 1, callValues[0])
	}()
}

func TestQueueAndError(t *testing.T) {
	var mu sync.Mutex
	callValues := []int64{}
	RegisterRecalc(-101, func(ctx context.Context, i int64, _ optional.Option[timeutil.TimeStamp]) error {
		mu.Lock()
		defer mu.Unlock()
		callValues = append(callValues, i)
		return errors.New("don't like that value")
	})

	err := safePush(recalcRequest{
		RecalcType: -101,
		ObjectID:   1,
	})
	require.NoError(t, err)

	for range 3 { // ensure object isn't requeued by flushing multiple times
		require.NoError(t, Flush(t.Context()))
	}
	func() {
		mu.Lock()
		defer mu.Unlock()
		assert.Len(t, callValues, 1)
		assert.EqualValues(t, 1, callValues[0])
	}()
}
