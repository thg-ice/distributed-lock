package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/thg-ice/distributed-lock/mock_gcs"
)

func ExampleLock() {
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()

	bucketName := "testing"
	if v, ok := os.LookupEnv("GCS_BUCKET"); ok {
		bucketName = v
	}

	var client *storage.Client
	if _, ok := os.LookupEnv("NO_MOCK"); ok {
		var err error
		client, err = storage.NewClient(ctx)
		if err != nil {
			panic(err)
		}
	} else {
		server := mock_gcs.NewServer(bucketName)
		defer server.Close()

		var err error
		client, err = server.Client(ctx)
		if err != nil {
			panic(err)
		}
	}

	l := NewLock(client.Bucket(bucketName), "pod-uuid", "path/to/lock/file.lock", 5*time.Minute, func(context.Context) Logger {
		return stderrLogger{}
	})

	// Acquire the lock
	if err := l.Lock(ctx, 30*time.Second); err != nil {
		// Action would need to be taken here if the lock couldn't be taken, such as re-queuing a reconcile request
		panic(err)
	}

	// Make sure the lock is released so others can take it
	defer func() {
		if err := l.Unlock(ctx); err != nil {
			panic(err)
		}
	}()

	// Regularly refresh the lock. If we're told that the lock had to be abandoned, then cancel the context so any
	// in-process work is stopped.
	go func() {
		t := time.Tick(2 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t:
				err := l.RefreshLock(ctx)
				if err != nil {
					if errors.Is(err, ErrLockAbandoned) {
						cancel()
					}
					fmt.Printf("Failed to refresh the lock: %s", err)
				}
			}
		}
	}()
	fmt.Println("hello")
	// Output: hello
}

var _ Logger = stderrLogger{}

type stderrLogger struct {
}

func (n stderrLogger) Info(msg string, keysAndValues ...any) {
	if len(keysAndValues)%2 != 0 {
		panic("Incorrect number of parameters")
	}

	_, _ = fmt.Fprintf(os.Stderr, "INFO: %s\n", msg)
}

func (n stderrLogger) Error(err error, msg string, keysAndValues ...any) {
	if len(keysAndValues)%2 != 0 {
		panic("Incorrect number of parameters")
	}

	_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s: %s\n", err, msg)
}
