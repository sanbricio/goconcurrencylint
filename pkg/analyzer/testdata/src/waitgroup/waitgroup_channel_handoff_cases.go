package waitgroup

import "sync"

// ========== the counter travels inside the value sent on a channel ==========

type writeBatch struct {
	wg      sync.WaitGroup
	entries int
	err     error
}

type batchWriter struct {
	batchCh chan *writeBatch
}

// GoodBatchCarriesWaitGroupToWorker: the batch handed to the writer carries its
// own WaitGroup, so the worker that receives it owns the matching Done.
func (w *batchWriter) GoodBatchCarriesWaitGroupToWorker(b *writeBatch) error {
	b.wg.Add(1)
	w.batchCh <- b
	b.wg.Wait()
	return b.err
}

func (w *batchWriter) drain() {
	for b := range w.batchCh {
		b.entries++
		b.wg.Done()
	}
}

// BadBatchSentToLocalChannelNeverDone: the channel is created and abandoned
// here, so nothing can receive the batch and call Done.
func BadBatchSentToLocalChannelNeverDone(b *writeBatch) {
	localCh := make(chan *writeBatch, 1)
	b.wg.Add(1) // want "waitgroup 'b.wg' has Add without corresponding Done"
	localCh <- b
	b.wg.Wait()
}

// ========== Add taken back when the handoff cannot proceed ==========

type splitIter struct{ n int }

func (i *splitIter) Seek(k int)  {}
func (i *splitIter) Valid() bool { return i.n > 0 }
func (i *splitIter) Next()       { i.n-- }

type splitter struct {
	taskWg *sync.WaitGroup
	taskCh chan int
	it     *splitIter
}

// GoodAddReturnedWhenChannelFull: the worker releases the task it is running
// with its own deferred Done, and the extra work it splits off carries a fresh
// Add — handed to whoever receives it, or given straight back when the channel
// cannot take it.
func (s *splitter) GoodAddReturnedWhenChannelFull(task int) (retErr error) {
	processed := 0

	defer func() {
		s.taskWg.Done()
		processed++
	}()

	const checkInterval = 1000
	iterations := 0

	for s.it.Seek(task); s.it.Valid(); s.it.Next() {
		processed++
		iterations++
		if iterations%checkInterval == 0 {
			if len(s.taskCh) == 0 {
				s.taskWg.Add(1)
				select {
				case s.taskCh <- task + iterations:
				default:
					s.taskWg.Done()
				}
			}
		}
	}
	return nil
}

// BadAddNotReturnedWhenChannelFull: nothing gives the count back on the path
// where the send does not happen, so the Add can be left dangling.
func (s *splitter) BadAddNotReturnedWhenChannelFull(task int) {
	for i := range 8 {
		s.taskWg.Add(1) // want "waitgroup 's.taskWg' has Add without corresponding Done"
		select {
		case s.taskCh <- task + i:
		default:
		}
	}
	s.taskWg.Wait()
}
