package copperd

import (
	"sync"
)

type server struct {
	lock sync.Mutex
}
