package copper

func decrementCounterUint32(counters map[uint32]int, key uint32) bool {
	newValue := counters[key] - 1
	if newValue != 0 {
		counters[key] = newValue
		return false
	}
	delete(counters, key)
	return true
}
