package main

import (
	"runtime"
	"sync"
)

var bufPool = &sync.Pool{
	New: func() interface{} {
		return make([]byte, 2048)
	},
}

func malloc(n int) []byte {
	return bufPool.Get().([]byte)[:n]
}

func free(bts []byte) {
	bufPool.Put(bts[:2048])
}

// This gives us ample buffer space to deal with CPU spikes and avoid packet loss.
var e2ejobs = make(chan func(), 1000)

func maybeDoJob(f func()) {
	select {
	case e2ejobs <- f:
	default:
	}
	//f()
}

func init() {
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		//log.Println("spawning worker thread", workerID)
		go func() {
			for i := 0; ; i++ {
				(<-e2ejobs)()
			}
		}()
	}
}
