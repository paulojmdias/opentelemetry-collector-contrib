// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	backoff "github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/featuregate"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/processor/resourcedetectionprocessor/internal/metadata"
)

// defaultRetryConfig returns a retry config suitable for tests: fast retries with no elapsed time limit.
func defaultRetryConfig() configretry.BackOffConfig {
	cfg := configretry.NewDefaultBackOffConfig()
	cfg.InitialInterval = 1 * time.Millisecond
	cfg.MaxInterval = 10 * time.Millisecond
	cfg.MaxElapsedTime = 0 // no limit; context controls the deadline
	return cfg
}

type mockDetector struct {
	mock.Mock
}

func (p *mockDetector) Detect(_ context.Context) (pcommon.Resource, string, error) {
	args := p.Called()
	return args.Get(0).(pcommon.Resource), args.String(1), args.Error(2)
}

type mockDetectorConfig struct{}

func (*mockDetectorConfig) GetConfigFromType(_ DetectorType) DetectorConfig {
	return nil
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name              string
		detectedResources []map[string]any
		expectedResource  map[string]any
	}{
		{
			name: "Detect three resources",
			detectedResources: []map[string]any{
				{"a": "1", "b": "2"},
				{"a": "11", "c": "3"},
				{"a": "12", "c": "3"},
			},
			expectedResource: map[string]any{"a": "1", "b": "2", "c": "3"},
		}, {
			name: "Detect empty resources",
			detectedResources: []map[string]any{
				{"a": "1", "b": "2"},
				{},
				{"a": "11"},
			},
			expectedResource: map[string]any{"a": "1", "b": "2"},
		}, {
			name: "Detect non-string resources",
			detectedResources: []map[string]any{
				{"bool": true, "int": int64(2), "double": 0.5},
				{"bool": false},
				{"a": "11"},
			},
			expectedResource: map[string]any{"a": "11", "bool": true, "int": int64(2), "double": 0.5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDetectors := make(map[DetectorType]DetectorFactory, len(tt.detectedResources))
			mockDetectorTypes := make([]DetectorType, 0, len(tt.detectedResources))

			for i, resAttrs := range tt.detectedResources {
				md := &mockDetector{}
				res := pcommon.NewResource()
				require.NoError(t, res.Attributes().FromRaw(resAttrs))
				md.On("Detect").Return(res, "", nil)

				mockDetectorType := DetectorType(fmt.Sprintf("mockDetector%v", i))
				mockDetectors[mockDetectorType] = func(processor.Settings, DetectorConfig) (Detector, error) {
					return md, nil
				}
				mockDetectorTypes = append(mockDetectorTypes, mockDetectorType)
			}

			f := NewProviderFactory(mockDetectors)
			p, err := f.CreateResourceProvider(processortest.NewNopSettings(metadata.Type), time.Second, defaultRetryConfig(), &mockDetectorConfig{}, mockDetectorTypes...)
			require.NoError(t, err)

			// Perform initial detection
			err = p.Refresh(t.Context(), &http.Client{Timeout: 10 * time.Second})
			require.NoError(t, err)

			// Get the detected resource
			got, _, err := p.Get(t.Context(), &http.Client{Timeout: 10 * time.Second})
			require.NoError(t, err)

			assert.Equal(t, tt.expectedResource, got.Attributes().AsRaw())
		})
	}
}

func TestDetectResource_InvalidDetectorType(t *testing.T) {
	mockDetectorKey := DetectorType("mock")
	p := NewProviderFactory(map[DetectorType]DetectorFactory{})
	_, err := p.CreateResourceProvider(processortest.NewNopSettings(metadata.Type), time.Second, defaultRetryConfig(), &mockDetectorConfig{}, mockDetectorKey)
	require.EqualError(t, err, fmt.Sprintf("invalid detector key: %v", mockDetectorKey))
}

func TestDetectResource_DetectorFactoryError(t *testing.T) {
	mockDetectorKey := DetectorType("mock")
	p := NewProviderFactory(map[DetectorType]DetectorFactory{
		mockDetectorKey: func(processor.Settings, DetectorConfig) (Detector, error) {
			return nil, errors.New("creation failed")
		},
	})
	_, err := p.CreateResourceProvider(processortest.NewNopSettings(metadata.Type), time.Second, defaultRetryConfig(), &mockDetectorConfig{}, mockDetectorKey)
	require.EqualError(t, err, fmt.Sprintf("failed creating detector type %q: %v", mockDetectorKey, "creation failed"))
}

func TestDetectResource_Error_ContextDeadline_WithErrPropagation(t *testing.T) {
	err := featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), true)
	assert.NoError(t, err)

	md1 := &mockDetector{}
	md1.On("Detect").Return(pcommon.NewResource(), "", errors.New("err1"))

	md2 := &mockDetector{}
	md2.On("Detect").Return(pcommon.NewResource(), "", errors.New("err2"))

	p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md1, md2)

	var cancel context.CancelFunc
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	err = p.Refresh(ctx, &http.Client{Timeout: 10 * time.Second})
	require.Error(t, err)
	require.Contains(t, err.Error(), "err1")
	require.Contains(t, err.Error(), "err2")
}

func TestMergeResource(t *testing.T) {
	for _, tt := range []struct {
		name       string
		res1       map[string]any
		res2       map[string]any
		overrideTo bool
		expected   map[string]any
	}{
		{
			name:       "override non-empty resources",
			res1:       map[string]any{"a": "11", "b": "2"},
			res2:       map[string]any{"a": "1", "c": "3"},
			overrideTo: true,
			expected:   map[string]any{"a": "1", "b": "2", "c": "3"},
		}, {
			name:       "empty resource",
			res1:       map[string]any{},
			res2:       map[string]any{"a": "1", "c": "3"},
			overrideTo: false,
			expected:   map[string]any{"a": "1", "c": "3"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res1 := pcommon.NewResource()
			require.NoError(t, res1.Attributes().FromRaw(tt.res1))
			res2 := pcommon.NewResource()
			require.NoError(t, res2.Attributes().FromRaw(tt.res2))
			MergeResource(res1, res2, tt.overrideTo)
			assert.Equal(t, tt.expected, res1.Attributes().AsRaw())
		})
	}
}

func TestMergeResourceZeroValueFrom(t *testing.T) {
	t.Parallel()

	to := pcommon.NewResource()
	require.NoError(t, to.Attributes().FromRaw(map[string]any{"keep": "me"}))

	assert.NotPanics(t, func() {
		MergeResource(to, pcommon.Resource{}, false)
	})
	assert.Equal(t, map[string]any{"keep": "me"}, to.Attributes().AsRaw())
}

func TestIsEmptyResourceZeroValue(t *testing.T) {
	t.Parallel()

	assert.True(t, IsEmptyResource(pcommon.Resource{}))
}

type mockParallelDetector struct {
	mock.Mock
	ch chan struct{}
}

func newMockParallelDetector() *mockParallelDetector {
	return &mockParallelDetector{ch: make(chan struct{}, 1)}
}

func (p *mockParallelDetector) Detect(_ context.Context) (pcommon.Resource, string, error) {
	<-p.ch
	args := p.Called()
	return args.Get(0).(pcommon.Resource), args.String(1), args.Error(2)
}

// TestDetectResource_Parallel validates that multiple concurrent calls to Get
// return the cached result after initial Refresh
func TestDetectResource_Parallel(t *testing.T) {
	const iterations = 5

	md1 := newMockParallelDetector()
	res1 := pcommon.NewResource()
	require.NoError(t, res1.Attributes().FromRaw(map[string]any{"a": "1", "b": "2"}))
	md1.On("Detect").Return(res1, "", nil)

	md2 := newMockParallelDetector()
	res2 := pcommon.NewResource()
	require.NoError(t, res2.Attributes().FromRaw(map[string]any{"a": "11", "c": "3"}))
	md2.On("Detect").Return(res2, "", nil)

	expectedResourceAttrs := map[string]any{"a": "1", "b": "2", "c": "3"}

	p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md1, md2)

	// Perform initial detection
	go func() {
		time.Sleep(5 * time.Millisecond)
		md1.ch <- struct{}{}
		md2.ch <- struct{}{}
	}()

	err := p.Refresh(t.Context(), &http.Client{Timeout: 10 * time.Second})
	require.NoError(t, err)

	// Get the detected resource
	detected, _, err := p.Get(t.Context(), &http.Client{Timeout: 10 * time.Second})
	require.NoError(t, err)
	require.Equal(t, expectedResourceAttrs, detected.Attributes().AsRaw())

	// Verify Detect was called once during Refresh
	md1.AssertNumberOfCalls(t, "Detect", 1)
	md2.AssertNumberOfCalls(t, "Detect", 1)

	// Now call Get multiple times concurrently - should return cached value
	wg := &sync.WaitGroup{}
	wg.Add(iterations)
	for range iterations {
		go func() {
			defer wg.Done()
			detected, _, err := p.Get(t.Context(), &http.Client{Timeout: 10 * time.Second})
			assert.NoError(t, err)
			assert.Equal(t, expectedResourceAttrs, detected.Attributes().AsRaw())
		}()
	}

	wg.Wait()

	// Verify Detect still only called once (not called again by Get)
	md1.AssertNumberOfCalls(t, "Detect", 1)
	md2.AssertNumberOfCalls(t, "Detect", 1)
}

func TestDetectResource_Reconnect(t *testing.T) {
	md1 := &mockDetector{}
	res1 := pcommon.NewResource()
	require.NoError(t, res1.Attributes().FromRaw(map[string]any{"a": "1", "b": "2"}))
	md1.On("Detect").Return(pcommon.NewResource(), "", errors.New("connection error1")).Twice()
	md1.On("Detect").Return(res1, "", nil)

	md2 := &mockDetector{}
	res2 := pcommon.NewResource()
	require.NoError(t, res2.Attributes().FromRaw(map[string]any{"c": "3"}))
	md2.On("Detect").Return(pcommon.NewResource(), "", errors.New("connection error2")).Once()
	md2.On("Detect").Return(res2, "", nil)

	expectedResourceAttrs := map[string]any{"a": "1", "b": "2", "c": "3"}

	p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md1, md2)

	err := p.Refresh(t.Context(), &http.Client{Timeout: 15 * time.Second})
	assert.NoError(t, err)

	// Get the detected resource
	detected, _, err := p.Get(t.Context(), &http.Client{Timeout: 15 * time.Second})
	assert.NoError(t, err)
	assert.Equal(t, expectedResourceAttrs, detected.Attributes().AsRaw())

	md1.AssertNumberOfCalls(t, "Detect", 3) // 2 errors + 1 success
	md2.AssertNumberOfCalls(t, "Detect", 2) // 1 error + 1 success
}

func TestResourceProvider_RefreshInterval(t *testing.T) {
	md := &mockDetector{}
	res1 := pcommon.NewResource()
	require.NoError(t, res1.Attributes().FromRaw(map[string]any{"a": "1"}))
	res2 := pcommon.NewResource()
	require.NoError(t, res2.Attributes().FromRaw(map[string]any{"a": "2"}))

	// First call -> res1, second call -> res2
	md.On("Detect").Return(res1, "", nil).Once()
	md.On("Detect").Return(res2, "", nil).Once()

	p := NewResourceProvider(zap.NewNop(), 1*time.Second, defaultRetryConfig(), md)

	// Initial detection
	err := p.Refresh(t.Context(), &http.Client{Timeout: time.Second})
	require.NoError(t, err)

	got, _, err := p.Get(t.Context(), &http.Client{Timeout: time.Second})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": "1"}, got.Attributes().AsRaw())

	// Simulate a single periodic refresh
	err = p.Refresh(t.Context(), &http.Client{Timeout: time.Second})
	require.NoError(t, err)

	// The cached resource should now be updated
	got, _, err = p.Get(t.Context(), &http.Client{Timeout: time.Second})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"a": "2"}, got.Attributes().AsRaw())

	// Exactly two detections total: one initial + one refresh
	md.AssertNumberOfCalls(t, "Detect", 2)
}

func TestMergeSchemaURL(t *testing.T) {
	tests := []struct {
		name              string
		currentSchemaURL  string
		newSchemaURL      string
		expectedSchemaURL string
	}{
		{
			name:              "both empty",
			currentSchemaURL:  "",
			newSchemaURL:      "",
			expectedSchemaURL: "",
		},
		{
			name:              "current empty, new has value",
			currentSchemaURL:  "",
			newSchemaURL:      "https://opentelemetry.io/schemas/1.9.0",
			expectedSchemaURL: "https://opentelemetry.io/schemas/1.9.0",
		},
		{
			name:              "current has value, new empty",
			currentSchemaURL:  "https://opentelemetry.io/schemas/1.8.0",
			newSchemaURL:      "",
			expectedSchemaURL: "https://opentelemetry.io/schemas/1.8.0",
		},
		{
			name:              "same schema URLs",
			currentSchemaURL:  "https://opentelemetry.io/schemas/1.9.0",
			newSchemaURL:      "https://opentelemetry.io/schemas/1.9.0",
			expectedSchemaURL: "https://opentelemetry.io/schemas/1.9.0",
		},
		{
			name:              "different schema URLs - keeps current",
			currentSchemaURL:  "https://opentelemetry.io/schemas/1.8.0",
			newSchemaURL:      "https://opentelemetry.io/schemas/1.9.0",
			expectedSchemaURL: "https://opentelemetry.io/schemas/1.8.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeSchemaURL(tt.currentSchemaURL, tt.newSchemaURL)
			assert.Equal(t, tt.expectedSchemaURL, result)
		})
	}
}

func TestIsEmptyResource(t *testing.T) {
	t.Run("empty resource", func(t *testing.T) {
		res := pcommon.NewResource()
		assert.True(t, IsEmptyResource(res))
	})

	t.Run("non-empty resource", func(t *testing.T) {
		res := pcommon.NewResource()
		res.Attributes().PutStr("key", "value")
		assert.False(t, IsEmptyResource(res))
	})
}

// TestRetryDisabled verifies that with retry disabled the detector is called exactly once,
// even if it returns an error.
func TestRetryDisabled(t *testing.T) {
	md := &mockDetector{}
	md.On("Detect").Return(pcommon.NewResource(), "", errors.New("transient error"))

	retryCfg := configretry.NewDefaultBackOffConfig()
	retryCfg.Enabled = false
	p := NewResourceProvider(zap.NewNop(), time.Second, retryCfg, md)

	err := featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), true)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), false)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	detectErr := p.Refresh(ctx, &http.Client{})
	require.Error(t, detectErr)

	// Detect must have been called exactly once (no retries).
	md.AssertNumberOfCalls(t, "Detect", 1)
}

// TestRetrySucceedsOnSecondAttempt verifies that if a detector fails once then succeeds,
// the ResourceProvider retries and ultimately returns a successful result.
func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	md := &mockDetector{}
	res := pcommon.NewResource()
	require.NoError(t, res.Attributes().FromRaw(map[string]any{"k": "v"}))
	md.On("Detect").Return(pcommon.NewResource(), "", errors.New("transient")).Once()
	md.On("Detect").Return(res, "", nil)

	p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := p.Refresh(ctx, &http.Client{})
	require.NoError(t, err)

	got, _, err := p.Get(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "v"}, got.Attributes().AsRaw())
	md.AssertNumberOfCalls(t, "Detect", 2)
}

// TestPermanentError verifies that a detector returning backoff.Permanent(err) is not retried.
func TestPermanentError(t *testing.T) {
	md := &mockDetector{}
	permErr := backoff.Permanent(errors.New("config is broken"))
	md.On("Detect").Return(pcommon.NewResource(), "", permErr)

	p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md)

	err := featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), true)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), false)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	detectErr := p.Refresh(ctx, &http.Client{})
	require.Error(t, detectErr)
	assert.Contains(t, detectErr.Error(), "config is broken")

	// Despite retry being enabled, Detect must be called exactly once
	// because permanent errors stop the retry loop immediately.
	md.AssertNumberOfCalls(t, "Detect", 1)
}

// TestMaxElapsedTime verifies that retry.max_elapsed_time bounds the total retry duration.
func TestMaxElapsedTime(t *testing.T) {
	md := &mockDetector{}
	md.On("Detect").Return(pcommon.NewResource(), "", errors.New("always fails"))

	retryCfg := configretry.NewDefaultBackOffConfig()
	retryCfg.InitialInterval = 1 * time.Millisecond
	retryCfg.MaxInterval = 5 * time.Millisecond
	retryCfg.MaxElapsedTime = 50 * time.Millisecond // stop after 50ms total

	err := featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), true)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = featuregate.GlobalRegistry().Set(metadata.ProcessorResourcedetectionPropagateerrorsFeatureGate.ID(), false)
	})

	p := NewResourceProvider(zap.NewNop(), time.Second, retryCfg, md)

	start := time.Now()
	detectErr := p.Refresh(t.Context(), &http.Client{})
	elapsed := time.Since(start)

	require.Error(t, detectErr)
	// Should have stopped well within 1 second due to max_elapsed_time=50ms.
	assert.Less(t, elapsed, 1*time.Second, "retry loop should have stopped after max_elapsed_time")
	// Should have retried at least once.
	assert.GreaterOrEqual(t, len(md.Calls), 2, "expected at least 2 Detect calls before giving up")
}

// TestRefreshKeepsPreviousOnFailure verifies that when a periodic refresh fails after a
// successful initial detection, the previous resource snapshot is preserved and Refresh
// returns nil (graceful degradation, not a hard failure).
func TestRefreshKeepsPreviousOnFailure(t *testing.T) {
	md := &mockDetector{}
	res := pcommon.NewResource()
	require.NoError(t, res.Attributes().FromRaw(map[string]any{"k": "v"}))
	// First call succeeds.
	md.On("Detect").Return(res, "", nil).Once()
	// Subsequent calls always fail.
	md.On("Detect").Return(pcommon.NewResource(), "", errors.New("network down"))

	p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md)

	// Initial detection succeeds.
	err := p.Refresh(t.Context(), &http.Client{})
	require.NoError(t, err)

	got, _, err := p.Get(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "v"}, got.Attributes().AsRaw())

	// Refresh fails — retry loop exhausts quickly via context timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err = p.Refresh(ctx, &http.Client{})
	// Must return nil: the provider gracefully keeps the previous snapshot.
	require.NoError(t, err)

	// The cached resource must still be the original one.
	got, _, err = p.Get(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "v"}, got.Attributes().AsRaw())
}

func TestStartStopRefreshing(t *testing.T) {
	t.Run("with refresh interval", func(t *testing.T) {
		md := &mockDetector{}
		res1 := pcommon.NewResource()
		require.NoError(t, res1.Attributes().FromRaw(map[string]any{"a": "1"}))
		res2 := pcommon.NewResource()
		require.NoError(t, res2.Attributes().FromRaw(map[string]any{"a": "2"}))

		// First call returns res1, subsequent calls return res2
		md.On("Detect").Return(res1, "", nil).Once()
		md.On("Detect").Return(res2, "", nil)

		p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md)

		// Initial detection
		err := p.Refresh(t.Context(), &http.Client{Timeout: time.Second})
		require.NoError(t, err)

		got, _, err := p.Get(t.Context(), &http.Client{Timeout: time.Second})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"a": "1"}, got.Attributes().AsRaw())

		// Start refreshing with a short interval
		p.StartRefreshing(100*time.Millisecond, &http.Client{Timeout: time.Second})

		// Wait for at least one refresh cycle
		time.Sleep(250 * time.Millisecond)

		// Stop refreshing
		p.StopRefreshing()

		// Get should now return updated resource
		got, _, err = p.Get(t.Context(), &http.Client{Timeout: time.Second})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"a": "2"}, got.Attributes().AsRaw())

		// Verify Detect was called at least twice (initial + at least one refresh)
		assert.GreaterOrEqual(t, len(md.Calls), 2, "Expected at least 2 calls to Detect")
	})

	t.Run("with zero refresh interval", func(t *testing.T) {
		md := &mockDetector{}
		res := pcommon.NewResource()
		require.NoError(t, res.Attributes().FromRaw(map[string]any{"a": "1"}))
		md.On("Detect").Return(res, "", nil).Once()

		p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md)

		// Initial detection
		err := p.Refresh(t.Context(), &http.Client{Timeout: time.Second})
		require.NoError(t, err)

		// Start refreshing with zero interval - should not start goroutine
		p.StartRefreshing(0, &http.Client{Timeout: time.Second})

		// Wait a bit
		time.Sleep(100 * time.Millisecond)

		// Stop refreshing (should be safe even though nothing started)
		p.StopRefreshing()

		// Verify Detect was only called once (no periodic refreshes)
		md.AssertNumberOfCalls(t, "Detect", 1)
	})

	t.Run("stop without start", func(t *testing.T) {
		md := &mockDetector{}
		res := pcommon.NewResource()
		require.NoError(t, res.Attributes().FromRaw(map[string]any{"a": "1"}))
		md.On("Detect").Return(res, "", nil).Once()

		p := NewResourceProvider(zap.NewNop(), time.Second, defaultRetryConfig(), md)

		// Initial detection
		err := p.Refresh(t.Context(), &http.Client{Timeout: time.Second})
		require.NoError(t, err)

		// Stop refreshing without ever starting - should be safe
		p.StopRefreshing()

		// Verify Detect was only called once
		md.AssertNumberOfCalls(t, "Detect", 1)
	})
}
