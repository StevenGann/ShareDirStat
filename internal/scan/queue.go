package scan

import "sync"

// dirTask is one directory waiting to be read.
type dirTask struct {
	path    string // absolute path
	relPath string // share-relative path, "" for the scan root
	node    uint32
	depth   int
}

// workQueue is a LIFO task pool shared by a scan's workers.
//
// LIFO keeps the crawl depth-first, which bounds the queue size and keeps
// directory entries hot in the page cache. Completion is detected when every
// worker is idle and the stack is empty; pausing simply stops workers from
// pulling new work while in-flight directories finish (FR-SCAN-10).
type workQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	stack   []dirTask
	workers int
	idle    int
	paused  bool
	closed  bool
}

func newWorkQueue(workers int) *workQueue {
	q := &workQueue{workers: workers, stack: make([]dirTask, 0, 1024)}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push adds a directory to the queue.
func (q *workQueue) push(t dirTask) {
	q.mu.Lock()
	if !q.closed {
		q.stack = append(q.stack, t)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

// pop blocks until work is available, returning false once the crawl is
// complete or the queue has been closed by a cancellation.
func (q *workQueue) pop() (dirTask, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.closed {
			return dirTask{}, false
		}
		if len(q.stack) > 0 && !q.paused {
			t := q.stack[len(q.stack)-1]
			q.stack = q.stack[:len(q.stack)-1]
			return t, true
		}
		q.idle++
		if len(q.stack) == 0 && q.idle == q.workers {
			q.closed = true
			q.idle--
			q.cond.Broadcast()
			return dirTask{}, false
		}
		q.cond.Wait()
		q.idle--
	}
}

// close wakes every worker and abandons any queued work.
func (q *workQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.stack = q.stack[:0]
	q.cond.Broadcast()
	q.mu.Unlock()
}

// setPaused stops or resumes handing out new directories.
func (q *workQueue) setPaused(p bool) {
	q.mu.Lock()
	q.paused = p
	if !p {
		q.cond.Broadcast()
	}
	q.mu.Unlock()
}

// pending returns the queue depth (progress reporting).
func (q *workQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.stack)
}
