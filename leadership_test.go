package lock

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thg-ice/distributed-lock/mock_gcs"
)

func TestLock_Lock(t *testing.T) {
	tests := []struct {
		name                string
		skipInitialObject   bool
		initialObjectOwner  string
		initialObjectExpiry time.Duration
		expectedErr         string
		expectedObj         *storage.ObjectAttrs
	}{
		{
			name:              "creates-lock-when-not-present",
			skipInitialObject: true,
			expectedObj: &storage.ObjectAttrs{
				Bucket:       "b",
				Name:         "testing",
				CacheControl: "no-store",
				Metadata: map[string]string{
					ownerMetadata: "id",
				},
				Generation:     1,
				Metageneration: 1,
			},
		},
		{
			name:                "cannot-lock-if-already-present",
			initialObjectOwner:  "someone-else",
			initialObjectExpiry: 3 * time.Minute,
			expectedErr:         "got HTTP response code 412 with body: createObject already has that object",
			expectedObj: &storage.ObjectAttrs{
				Bucket:       "b",
				Name:         "testing",
				CacheControl: "no-store",
				Metadata: map[string]string{
					ownerMetadata: "someone-else",
				},
				Generation:     1,
				Metageneration: 6,
			},
		},
		{
			name:                "removes-lock-if-expired",
			initialObjectOwner:  "someone-else",
			initialObjectExpiry: -4 * time.Minute,
			expectedObj: &storage.ObjectAttrs{
				Bucket:       "b",
				Name:         "testing",
				CacheControl: "no-store",
				Metadata: map[string]string{
					ownerMetadata: "id",
				},
				Generation:     1,
				Metageneration: 1,
			},
		},
		{
			name:                "removes-lock-if-owned-by-me",
			initialObjectOwner:  "id",
			initialObjectExpiry: 10 * time.Minute,
			expectedObj: &storage.ObjectAttrs{
				Bucket:       "b",
				Name:         "testing",
				CacheControl: "no-store",
				Metadata: map[string]string{
					ownerMetadata: "id",
				},
				Generation:     1,
				Metageneration: 1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mock := mock_gcs.NewServer("b", mock_gcs.WithFailOnObjectExistence())
			if !test.skipInitialObject {
				mock.Add("testing", storage.ObjectAttrs{
					Bucket: "b",
					Name:   "testing",
					Metadata: map[string]string{
						ownerMetadata:     test.initialObjectOwner,
						expiresAtMetadata: time.Now().UTC().Add(test.initialObjectExpiry).Format(time.RFC3339Nano),
					},
					Generation:     1,
					Metageneration: 6,
					CacheControl:   "no-store",
				})
			}
			t.Cleanup(mock.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			t.Cleanup(cancel)

			client, err := mock.Client(ctx)
			require.NoError(t, err)

			ttl := 3 * time.Minute

			subject := NewLock(client.Bucket("b"), "id", "testing", ttl, func(context.Context) Logger {
				return loggerToTestingT{t}
			})

			err = subject.Lock(ctx, 500*time.Millisecond)

			if test.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, test.expectedErr)
			}

			actualObj := mock.Get("testing")
			require.NotNil(t, actualObj)

			assert.Contains(t, actualObj.Metadata, expiresAtMetadata)
			expiresAt, err := time.Parse(time.RFC3339Nano, actualObj.Metadata[expiresAtMetadata])
			require.NoError(t, err)

			assert.WithinDuration(t, expiresAt, time.Now().UTC().Add(ttl), 1*time.Minute)

			delete(actualObj.Metadata, expiresAtMetadata)

			assert.Equal(t, test.expectedObj, actualObj)
		})
	}
}

func TestLock_RefreshLock(t *testing.T) {
	tests := []struct {
		name                   string
		refreshMetadata        bool
		skipInitialObject      bool
		objectMetageneration   int64
		initialMetageneration  int64
		initialFailures        uint
		expectedErr            string
		expectedMetageneration int64
		expectedTTLUpdated     bool
		mockGcsOptions         []mock_gcs.Opt
	}{
		{
			name:                   "updates-expires-at",
			refreshMetadata:        true,
			objectMetageneration:   2,
			initialMetageneration:  2,
			initialFailures:        0,
			expectedErr:            "",
			expectedMetageneration: 3,
			expectedTTLUpdated:     true,
		},
		{
			name:                   "lock-abandoned-if-metageneration-incorrect",
			refreshMetadata:        true,
			objectMetageneration:   1,
			initialMetageneration:  2,
			initialFailures:        0,
			expectedErr:            ErrLockAbandoned.Error(),
			expectedMetageneration: 1,
			expectedTTLUpdated:     false,
		},
		{
			name:                  "lock-abandoned-if-missing-object",
			refreshMetadata:       true,
			skipInitialObject:     true,
			objectMetageneration:  1,
			initialMetageneration: 1,
			initialFailures:       0,
			expectedErr:           ErrLockAbandoned.Error(),
		},
		{
			name:                   "doesn't-update-lock-if-too-many-failures",
			refreshMetadata:        true,
			objectMetageneration:   2,
			initialMetageneration:  2,
			initialFailures:        4,
			expectedErr:            ErrLockAbandoned.Error(),
			expectedMetageneration: 2,
			expectedTTLUpdated:     false,
		},
		{
			name:                   "doesn't-update-lock-if-refreshMetadata-not-set",
			refreshMetadata:        false,
			objectMetageneration:   2,
			initialMetageneration:  2,
			initialFailures:        0,
			expectedErr:            "",
			expectedMetageneration: 2,
			expectedTTLUpdated:     false,
		},
		{
			name:                   "lock-failure-increments-refreshFailures",
			refreshMetadata:        true,
			skipInitialObject:      false,
			objectMetageneration:   1,
			initialMetageneration:  1,
			initialFailures:        0,
			expectedErr:            "googleapi: got HTTP response code 418 with body: updateObject failed on name",
			expectedMetageneration: 1,
			expectedTTLUpdated:     false,
			mockGcsOptions:         []mock_gcs.Opt{mock_gcs.WithFailOnObjectName("testing")},
		},
		{
			name:                   "lock-failure-increments-refreshFailures",
			refreshMetadata:        true,
			skipInitialObject:      false,
			objectMetageneration:   1,
			initialMetageneration:  1,
			initialFailures:        3,
			expectedErr:            ErrLockAbandoned.Error(),
			expectedMetageneration: 1,
			expectedTTLUpdated:     false,
			mockGcsOptions:         []mock_gcs.Opt{mock_gcs.WithFailOnObjectName("testing")},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originalExpiresAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)

			mock := mock_gcs.NewServer("b", append([]mock_gcs.Opt{mock_gcs.WithFailOnObjectExistence()}, test.mockGcsOptions...)...)
			if !test.skipInitialObject {
				mock.Add("testing", storage.ObjectAttrs{
					Bucket: "b",
					Name:   "testing",
					Metadata: map[string]string{
						ownerMetadata:     "id",
						expiresAtMetadata: originalExpiresAt,
					},
					Generation:     2,
					Metageneration: test.objectMetageneration,
					CacheControl:   "no-store",
				})
			}
			t.Cleanup(mock.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			t.Cleanup(cancel)

			client, err := mock.Client(ctx)
			require.NoError(t, err)

			ttl := 3 * time.Minute

			subject := NewLock(client.Bucket("b"), "id", "testing", ttl, func(context.Context) Logger {
				return loggerToTestingT{t}
			})
			subject.refreshFailures = test.initialFailures
			subject.refreshMetadata = test.refreshMetadata
			subject.latestMetadataGeneration = test.initialMetageneration

			err = subject.RefreshLock(ctx)
			if test.expectedErr == "" {
				require.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, test.expectedErr)
			}

			updated := mock.Get("testing")

			if test.skipInitialObject {
				assert.Nil(t, updated)
				return
			}

			assert.Equal(t, test.expectedMetageneration, updated.Metageneration)
			assert.NotZero(t, updated.Generation)

			assert.Equal(t, "id", updated.Metadata[ownerMetadata])

			if test.expectedTTLUpdated {
				assert.NotEqual(t, originalExpiresAt, updated.Metadata[expiresAtMetadata])

				expiresAt, err := time.Parse(time.RFC3339Nano, updated.Metadata[expiresAtMetadata])
				require.NoError(t, err)

				assert.WithinDuration(t, expiresAt, time.Now().UTC().Add(ttl), 1*time.Minute)
			} else {
				assert.Equal(t, originalExpiresAt, updated.Metadata[expiresAtMetadata])
			}
		})
	}
}

func TestLock_Unlock(t *testing.T) {
	tests := []struct {
		name                  string
		skipInitialObject     bool
		objectOwner           string
		objectMetageneration  int64
		initialMetageneration int64
		expectedErr           error
		expectObjectToRemain  bool
	}{
		{
			name:                  "happy-path",
			objectOwner:           "id",
			objectMetageneration:  2,
			initialMetageneration: 2,
			expectedErr:           nil,
			expectObjectToRemain:  false,
		},
		{
			name:                  "missing-object-swallowed",
			skipInitialObject:     true,
			initialMetageneration: 3,
			expectedErr:           nil,
			expectObjectToRemain:  false,
		},
		{
			name:                  "incorrect-metageneration-swallowed",
			objectOwner:           "id",
			objectMetageneration:  2,
			initialMetageneration: 3,
			expectedErr:           nil,
			expectObjectToRemain:  true,
		},
		{
			name:                  "doesn't-unlock-lock-owned-by-someone-else",
			objectOwner:           "someone-else",
			objectMetageneration:  2,
			initialMetageneration: 2,
			expectedErr:           errLockOwnedBySomeoneElse,
			expectObjectToRemain:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mock := mock_gcs.NewServer("b", mock_gcs.WithFailOnObjectExistence())
			if !test.skipInitialObject {
				mock.Add("testing", storage.ObjectAttrs{
					Bucket: "b",
					Name:   "testing",
					Metadata: map[string]string{
						ownerMetadata:     test.objectOwner,
						expiresAtMetadata: time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
					},
					Metageneration: test.objectMetageneration,
					CacheControl:   "no-store",
				})
			}
			t.Cleanup(mock.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			t.Cleanup(cancel)

			client, err := mock.Client(ctx)
			require.NoError(t, err)

			ttl := 3 * time.Minute

			subject := NewLock(client.Bucket("b"), "id", "testing", ttl, func(context.Context) Logger {
				return loggerToTestingT{t}
			})
			subject.latestMetadataGeneration = test.initialMetageneration

			err = subject.Unlock(ctx)
			assert.Equal(t, test.expectedErr, err)

			if test.expectObjectToRemain {
				assert.NotNil(t, mock.Get("testing"))
			} else {
				assert.Nil(t, mock.Get("testing"))
			}
		})
	}
}

var _ Logger = loggerToTestingT{}

type loggerToTestingT struct {
	*testing.T
}

func (l loggerToTestingT) Info(msg string, keysAndValues ...any) {
	if len(keysAndValues)%2 != 0 {
		panic("Incorrect number of parameters")
	}
	l.Logf("INFO: %s, %#v", msg, keysAndValues)
}

func (l loggerToTestingT) Error(err error, msg string, keysAndValues ...any) {
	if len(keysAndValues)%2 != 0 {
		panic("Incorrect number of parameters")
	}
	l.Logf("ERROR: %s: %s, %#v", err, msg, keysAndValues)
}
