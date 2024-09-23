// Package lock provides a distributed locking algorithm backed by Google Cloud Storage. See
// https://www.joyfulbikeshedding.com/blog/2021-05-19-robust-distributed-locking-algorithm-based-on-google-cloud-storage.html
// for more details on the general design.
package lock

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

var (
	ErrLockAbandoned          = errors.New("lock abandoned")
	errLockOwnedBySomeoneElse = errors.New("unable to delete lock owned by someone else")
)

const (
	maxRefreshFailures = 3
	ownerMetadata      = "owner"
	expiresAtMetadata  = "expires-at"
)

// Lock provides a lock based off of a Google Cloud Storage object, without requiring communication between clients.
type Lock struct {
	bucket   *storage.BucketHandle
	path     string
	identity string
	ttl      time.Duration
	logger   func(ctx context.Context) Logger

	mutex           sync.Mutex
	refreshMetadata bool
	refreshFailures uint

	latestGeneration         int64
	latestMetadataGeneration int64
}

func NewLock(bucket *storage.BucketHandle, id, path string, ttl time.Duration, logContext func(context.Context) Logger) *Lock {
	return &Lock{
		bucket:                   bucket,
		path:                     path,
		identity:                 id,
		ttl:                      ttl,
		logger:                   logContext,
		mutex:                    sync.Mutex{},
		refreshMetadata:          false,
		latestMetadataGeneration: 1,
	}
}

type Logger interface {
	Info(msg string, keysAndValues ...any)
	Error(err error, msg string, keysAndValues ...any)
}

// Lock will attempt to acquire the configured lock until the context has timed out. The caller is expected to
// frequently call RefreshLock while holding the lock and Unlock when the lock is no longer needed.
func (l *Lock) Lock(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var errs []error
	for {
		select {
		case <-ctx.Done():
			return errors.Join(append(errs, ctx.Err())...)
		default:
			err := l.createLock(ctx)
			if err == nil {
				return nil
			}

			var gErr *googleapi.Error
			if errors.As(err, &gErr) && gErr.Code == http.StatusPreconditionFailed {
				if err := l.deleteLockIfStale(ctx); err != nil {
					return err
				}
			}
			l.logger(ctx).Error(err, "Failed to acquire lock", "path", l.path)
			errs = append(errs, err)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// Unlock will attempt to release the acquired lock.
func (l *Lock) Unlock(ctx context.Context) error {
	return l.deleteLock(ctx, nil, true)
}

// RefreshLock will update the information on the lock to ensure that the client still owns it. If ErrLockAbandoned is
// returned, then the client should assume the lock has been lost and stop immediately.
func (l *Lock) RefreshLock(ctx context.Context) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if !l.refreshMetadata {
		return nil
	}

	if l.refreshFailures > maxRefreshFailures {
		return ErrLockAbandoned
	}

	l.logger(ctx).Info("Refreshing lock", "path", l.path)

	attrs, err := l.bucket.Object(l.path).
		If(storage.Conditions{
			GenerationMatch:     l.latestGeneration,
			MetagenerationMatch: l.latestMetadataGeneration}).
		Update(ctx, storage.ObjectAttrsToUpdate{Metadata: l.metadata()})
	if err != nil {
		var gErr *googleapi.Error
		if errors.Is(err, storage.ErrObjectNotExist) ||
			(errors.As(err, &gErr) && gErr.Code == http.StatusPreconditionFailed) {
			return ErrLockAbandoned
		}
		l.refreshFailures++

		if l.refreshFailures > maxRefreshFailures {
			return ErrLockAbandoned
		}

		return err
	}

	l.refreshFailures = 0
	l.latestMetadataGeneration = attrs.Metageneration
	l.latestGeneration = attrs.Generation
	return nil
}

func (l *Lock) deleteLockIfStale(ctx context.Context) error {
	attrs, err := l.bucket.Object(l.path).Attrs(ctx)
	if err != nil {
		return err
	}

	if attrs.Metadata[ownerMetadata] == l.identity {
		if err := l.deleteLock(ctx, &attrs.Metageneration, false); err != nil {
			return err
		}
	}

	expires, err := time.Parse(time.RFC3339Nano, attrs.Metadata[expiresAtMetadata])
	if err != nil || time.Now().After(expires) {
		values := []any{"path", l.path}
		if err != nil {
			values = append(values, "err", err)
		}
		l.logger(ctx).Info("Lock expired", values...)
		return l.deleteLock(ctx, &attrs.Metageneration, false)
	}

	return nil
}

func (l *Lock) createLock(ctx context.Context) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	o := l.bucket.Object(l.path).If(storage.Conditions{DoesNotExist: true})

	w := o.NewWriter(ctx)
	w.CacheControl = "no-store"
	w.Metadata = l.metadata()

	if err := w.Close(); err != nil {
		return err
	}

	attrs, err := l.bucket.Object(l.path).Attrs(ctx)
	if err != nil {
		return err
	}

	l.refreshMetadata = true
	l.latestMetadataGeneration = attrs.Metageneration
	l.latestGeneration = attrs.Generation
	return nil
}

func (l *Lock) deleteLock(ctx context.Context, metageneration *int64, confirmOwner bool) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if confirmOwner {
		// Check we still own the lock, on the off chance that the metageneration of the new lock matches what we think
		// the old one is at.
		attrs, err := l.bucket.Object(l.path).Attrs(ctx)
		if err != nil {
			if errors.Is(err, storage.ErrObjectNotExist) {
				return nil
			}
			return err
		}

		if attrs.Metadata[ownerMetadata] != l.identity {
			return errLockOwnedBySomeoneElse
		}
	}

	l.refreshMetadata = false

	m := l.latestMetadataGeneration
	if metageneration != nil {
		m = *metageneration
	}

	if err := l.bucket.Object(l.path).If(storage.Conditions{MetagenerationMatch: m}).Delete(ctx); err != nil {
		var gErr *googleapi.Error
		if errors.Is(err, storage.ErrObjectNotExist) || (errors.As(err, &gErr) && gErr.Code == http.StatusPreconditionFailed) {
			// TODO what could a caller do if they get StatusPreconditionFailed?
			return nil
		}
		return err
	}

	return nil
}

func (l *Lock) metadata() map[string]string {
	ttl := time.Now().UTC().Add(l.ttl).Format(time.RFC3339Nano)

	return map[string]string{
		expiresAtMetadata: ttl,
		ownerMetadata:     l.identity,
	}
}
