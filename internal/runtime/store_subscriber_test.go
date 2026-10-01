package runtime

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestStoreConcurrentDisconnectAndPublish(t *testing.T) {
	store := NewStore(Snapshot{})
	var workers sync.WaitGroup
	start := make(chan struct{})
	for worker := range 8 {
		workers.Go(func() {
			<-start
			for iteration := range 1000 {
				updates, unsubscribe := store.Subscribe(1)
				store.Apply(Update{Source: fmt.Sprintf("%d/%d", worker, iteration)})
				unsubscribe()
				unsubscribe()
				for range updates {
				}
			}
		})
	}
	close(start)
	workers.Wait()
	store.Apply(Update{Source: "after disconnect"})
}

func TestStoreConcurrentPublishKeepsSequenceOrder(t *testing.T) {
	const publications = 2000
	store := NewStore(Snapshot{})
	updates, unsubscribe := store.Subscribe(publications)
	defer unsubscribe()
	latestUpdates, unsubscribeLatest := store.Subscribe(1)
	defer unsubscribeLatest()
	var publishers sync.WaitGroup
	start := make(chan struct{})
	for worker := range 8 {
		publishers.Go(func() {
			<-start
			for iteration := range publications / 8 {
				store.Apply(Update{Source: fmt.Sprintf("%d/%d", worker, iteration)})
			}
		})
	}
	close(start)
	publishers.Wait()
	select {
	case snapshot := <-latestUpdates:
		if snapshot.Sequence != publications {
			t.Fatalf("slow subscriber sequence = %d, want latest %d", snapshot.Sequence, publications)
		}
	default:
		t.Fatal("slow subscriber did not retain the latest publication")
	}
	for sequence := uint64(1); sequence <= publications; sequence++ {
		select {
		case snapshot := <-updates:
			if snapshot.Sequence != sequence {
				t.Fatalf("subscriber sequence = %d, want %d", snapshot.Sequence, sequence)
			}
		default:
			t.Fatalf("missing publication %d", sequence)
		}
	}
}

func TestStoreSlowSubscriberRetainsLatestWithoutBlocking(t *testing.T) {
	for _, buffer := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("buffer_%d", buffer), func(t *testing.T) {
			store := NewStore(Snapshot{})
			updates, unsubscribe := store.Subscribe(buffer)
			defer unsubscribe()
			completed := make(chan struct{})
			go func() {
				defer close(completed)
				for iteration := range 100 {
					store.Apply(Update{Source: fmt.Sprint(iteration)})
				}
			}()
			select {
			case <-completed:
			case <-time.After(5 * time.Second):
				t.Fatal("publication blocked on a slow subscriber")
			}
			latest := store.Current()
			if _, changed := store.Apply(Update{Source: "99"}); changed {
				t.Fatal("identical update published a new snapshot")
			}
			unsubscribe()
			unsubscribe()
			var last Snapshot
			var count int
			for snapshot := range updates {
				if snapshot.Sequence <= last.Sequence {
					t.Fatalf("non-increasing sequence: %d after %d", snapshot.Sequence, last.Sequence)
				}
				last = snapshot
				count++
			}
			capacity := max(buffer, 1)
			if count != capacity || last.Sequence != 100 || last.Source != "99" || !reflect.DeepEqual(last, latest) {
				t.Fatalf("retained %d snapshots, last = %+v; want %d ending with %+v", count, last, capacity, latest)
			}
			store.Apply(Update{Source: "after close"})
		})
	}
}
