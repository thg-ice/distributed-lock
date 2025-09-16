package mock_gcs // nolint:revive // Nothing wrong with underscore in a name

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

func TestServer_RemoveAll(t *testing.T) {
	subject := NewServer("b")
	subject.Add("b", storage.ObjectAttrs{})
	subject.RemoveAll()

	assert.Empty(t, subject.data)
}

func TestGCS_ListObjects(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		expectedError bool
		expected      []string
	}{
		{
			name:          "validates bucket",
			bucket:        "different",
			expectedError: true,
		},
		{
			name:          "gets objects",
			bucket:        "b",
			expectedError: false,
			expected:      []string{"prefix/first", "prefix/second"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := NewServer("b")
			subject.Add("not-prefixed", storage.ObjectAttrs{})

			t.Cleanup(subject.Close)

			subject.Add("not-prefixed", storage.ObjectAttrs{})
			subject.Add("prefix/first", storage.ObjectAttrs{})
			subject.Add("prefix/second", storage.ObjectAttrs{})

			client, err := subject.Client(context.Background())
			require.NoError(t, err)
			client.SetRetry(storage.WithMaxAttempts(1))

			it := client.Bucket(test.bucket).Objects(context.Background(), &storage.Query{Prefix: "prefix/"})
			var names []string
			for {
				attr, err := it.Next()
				if errors.Is(err, iterator.Done) {
					break
				}
				if test.expectedError {
					assert.Error(t, err)
					return
				}

				require.NoError(t, err)
				names = append(names, attr.Name)
			}

			if test.expectedError {
				require.Fail(t, "test should have errored")
			}

			assert.ElementsMatch(t, []string{"prefix/first", "prefix/second"}, names)
		})
	}

	subject := NewServer("b")
	subject.Add("not-prefixed", storage.ObjectAttrs{})

	t.Cleanup(subject.Close)

	client, err := subject.Client(context.Background())
	require.NoError(t, err)

	client.SetRetry(storage.WithMaxAttempts(1))

	w := client.Bucket("b").Object("name").NewWriter(context.Background())
	w.Metadata = map[string]string{"k": "v"}
	require.NoError(t, w.Close())

	subject.Get("name")
}

func TestGCS_CreateObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		objectName    string
		expectedError bool
		expected      storage.ObjectAttrs
	}{
		{
			name:          "validates bucket",
			bucket:        "different",
			objectName:    "unused",
			expectedError: true,
		},
		{
			name:          "saves object",
			bucket:        "b",
			objectName:    "name",
			expectedError: false,
			expected: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "name",
				CacheControl:   "",
				Metadata:       map[string]string{"k": "v"},
				Generation:     1,
				Metageneration: 1,
			},
		},
		{
			name:          "requires name",
			bucket:        "b",
			objectName:    "",
			expectedError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := NewServer("b")
			subject.Add("not-prefixed", storage.ObjectAttrs{})

			t.Cleanup(subject.Close)

			client, err := subject.Client(context.Background())
			require.NoError(t, err)

			client.SetRetry(storage.WithMaxAttempts(1))

			w := client.Bucket(test.bucket).Object(test.objectName).NewWriter(context.Background())
			w.Metadata = map[string]string{"k": "v"}

			err = w.Close()
			if test.expectedError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			actual := subject.Get(test.objectName)
			assert.Equal(t, test.expected, *actual)
		})
	}
}

func TestGCS_ReadObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		initialObject storage.ObjectAttrs
		objectName    string
		expectedError bool
		expected      storage.ObjectAttrs
	}{
		{
			name:          "validates bucket",
			bucket:        "different",
			objectName:    "not-important",
			expectedError: true,
		},
		{
			name:       "reads object",
			bucket:     "b",
			objectName: "object",
			initialObject: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "object",
				CacheControl:   "no-cache",
				Metadata:       map[string]string{"k": "v"},
				Generation:     5,
				Metageneration: 3,
			},
			expectedError: false,
			expected: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "object",
				CacheControl:   "no-cache",
				Metadata:       map[string]string{"k": "v"},
				Generation:     5,
				Metageneration: 3,
				MD5:            []byte{},
				Retention:      nil,
			},
		},
		{
			name:          "unknown object",
			bucket:        "b",
			objectName:    "missing",
			expectedError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := NewServer("b")
			subject.Add("object", test.initialObject)

			t.Cleanup(subject.Close)

			client, err := subject.Client(context.Background())
			require.NoError(t, err)

			client.SetRetry(storage.WithMaxAttempts(1))

			attrs, err := client.Bucket(test.bucket).Object(test.objectName).Attrs(context.Background())

			if test.expectedError {
				assert.Error(t, err)
				return
			}

			assert.Equal(t, test.expected, *attrs)
		})
	}
}

func TestGCS_UpdateObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		initialObject storage.ObjectAttrs
		objectName    string
		condition     *storage.Conditions
		expectedError bool
		expected      storage.ObjectAttrs
	}{
		{
			name:          "validates bucket",
			bucket:        "different",
			objectName:    "not-important",
			expectedError: true,
		},
		{
			name:       "updates object",
			bucket:     "b",
			objectName: "object",
			initialObject: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "object",
				CacheControl:   "no-cache",
				Metadata:       map[string]string{"k": "v"},
				Generation:     5,
				Metageneration: 3,
			},
			condition:     &storage.Conditions{MetagenerationMatch: 3},
			expectedError: false,
			expected: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "object",
				CacheControl:   "no-cache",
				Metadata:       map[string]string{"k": "v"},
				Generation:     5,
				Metageneration: 4,
			},
		},
		{
			name:       "fails to update object if wrong metageneration",
			bucket:     "b",
			objectName: "object",
			initialObject: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "object",
				CacheControl:   "no-cache",
				Metadata:       map[string]string{"k": "v"},
				Generation:     5,
				Metageneration: 3,
			},
			condition:     &storage.Conditions{MetagenerationMatch: 2},
			expectedError: true,
		},
		{
			name:          "requires metageneration condition",
			bucket:        "b",
			objectName:    "object",
			expectedError: true,
		},
		{
			name:          "unknown object",
			bucket:        "b",
			condition:     &storage.Conditions{MetagenerationMatch: 3},
			objectName:    "missing",
			expectedError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := NewServer("b")
			subject.Add("object", test.initialObject)

			t.Cleanup(subject.Close)

			client, err := subject.Client(context.Background())
			require.NoError(t, err)

			client.SetRetry(storage.WithMaxAttempts(1))

			object := client.Bucket(test.bucket).Object(test.objectName)
			if test.condition != nil {
				object = object.If(*test.condition)
			}
			attrs, err := object.
				Update(context.Background(), storage.ObjectAttrsToUpdate{Metadata: map[string]string{"k": "v"}})

			if test.expectedError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			if len(attrs.MD5) == 0 {
				// this doesn't get set to nil by the client
				attrs.MD5 = nil
			}
			require.Equal(t, test.expected, *attrs)

			actual := subject.Get(test.objectName)
			assert.Equal(t, test.expected, *actual)
		})
	}
}

func TestGCS_DeleteObject(t *testing.T) {
	tests := []struct {
		name          string
		bucket        string
		initialObject storage.ObjectAttrs
		objectName    string
		expectedError bool
	}{
		{
			name:          "validates bucket",
			bucket:        "different",
			objectName:    "not-important",
			expectedError: true,
		},
		{
			name:       "deletes object",
			bucket:     "b",
			objectName: "object",
			initialObject: storage.ObjectAttrs{
				Bucket:         "b",
				Name:           "object",
				CacheControl:   "no-cache",
				Metadata:       map[string]string{"k": "v"},
				Generation:     5,
				Metageneration: 3,
			},
			expectedError: false,
		},
		{
			name:          "unknown object",
			bucket:        "b",
			objectName:    "missing",
			expectedError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := NewServer("b")
			subject.Add("object", test.initialObject)

			t.Cleanup(subject.Close)

			client, err := subject.Client(context.Background())
			require.NoError(t, err)

			client.SetRetry(storage.WithMaxAttempts(1))

			err = client.Bucket(test.bucket).Object(test.objectName).
				Delete(context.Background())

			if test.expectedError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			assert.Nil(t, subject.Get(test.objectName))
		})
	}
}
