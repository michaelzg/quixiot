package roles

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Mixed struct {
	Poller     Poller
	Uploader   Uploader
	Publisher  Publisher
	Subscriber Subscriber
}

func (m Mixed) Run(ctx context.Context) error {
	if m.Poller.Client == nil {
		return fmt.Errorf("mixed: poller client is required")
	}
	if m.Uploader.Client == nil {
		return fmt.Errorf("mixed: uploader client is required")
	}
	if m.Publisher.Session == nil {
		return fmt.Errorf("mixed: publisher session is required")
	}
	if m.Subscriber.Session == nil {
		return fmt.Errorf("mixed: subscriber session is required")
	}

	errCh := make(chan error, 4)
	var wg sync.WaitGroup

	run := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}

	run("subscriber", m.Subscriber.Run)
	startTimer := time.NewTimer(100 * time.Millisecond)
	defer startTimer.Stop()
	select {
	case <-ctx.Done():
		<-doneChannel(&wg)
		return nil
	case <-startTimer.C:
	}
	run("publisher", m.Publisher.Run)
	run("poller", m.Poller.Run)
	run("uploader", m.Uploader.Run)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		<-done
		return nil
	case err := <-errCh:
		return err
	}
}

func doneChannel(wg *sync.WaitGroup) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}
