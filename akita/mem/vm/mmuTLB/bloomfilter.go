package mmuTLB

import (
	"hash/fnv"
)

type HashFunc func([]byte) uint64

type BloomFilter struct {
	BitArray  []bool
	HashFuncs []HashFunc
}

func NewBloomFilter(size int) *BloomFilter {
	hashFuncs := []HashFunc{
		func(data []byte) uint64 {
			h := fnv.New64a()
			h.Write(data)
			return h.Sum64()
		},
	}

	return &BloomFilter{
		BitArray:  make([]bool, size),
		HashFuncs: hashFuncs,
	}
}

func (f *BloomFilter) Add(data []byte) {
	for _, hashFunc := range f.HashFuncs {
		index := hashFunc(data) % uint64(len(f.BitArray))
		f.BitArray[index] = true
	}
}

func (f *BloomFilter) Check(data []byte) bool {
	for _, hashFunc := range f.HashFuncs {
		index := hashFunc(data) % uint64(len(f.BitArray))
		if !f.BitArray[index] {
			return false
		}
	}
	return true
}
