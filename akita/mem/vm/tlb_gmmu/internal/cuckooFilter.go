package internal

import (
	"math/rand"
	"time"
)

// 配置 Cuckoo Filter
const (
	BucketSize = 4   // 每个 bucket 存储的 vaddr 数量
	MaxKicks   = 500 // 允许的最大 kick 次数
	// 哈希魔数，用于混合位，降低哈希碰撞概率
	HashMagic = 0x9e3779b97f4a7c15
)

// Bucket 结构体，存储 vaddr（即 fingerprint）
type Bucket struct {
	entries [BucketSize]uint64
}

// CuckooFilter 结构体
type CuckooFilter struct {
	buckets []Bucket
	size    int
	name    string
}

// NewCuckooFilter 创建一个 Cuckoo Filter
func NewCuckooFilter(capacity int, name string) *CuckooFilter {
	rand.Seed(time.Now().UnixNano())
	return &CuckooFilter{
		buckets: make([]Bucket, capacity),
		size:    capacity,
		name:    name,
	}
}

// hash 计算 page vaddr 对应的两个哈希索引，并使用哈希魔数减少碰撞
func (cf *CuckooFilter) hash(vaddr uint64) (uint64, int, int) {
	// 使用哈希魔数混合输入位，生成 fingerprint
	fp := vaddr * HashMagic
	index1 := int(fp % uint64(cf.size))
	// 采用另一个魔数（2654435761）对 fp 进一步混合，计算第二个索引
	index2 := int((index1 ^ int((fp*2654435761)>>16)) % cf.size)
	return fp, index1, index2
}

// Insert 插入一个 vaddr
func (cf *CuckooFilter) Insert(vaddr uint64) bool {
	pageVaddr := cf.alignToPage(vaddr)
	fp, i1, i2 := cf.hash(pageVaddr)

	// 尝试插入到 i1 位置的 bucket
	if cf.insertIntoBucket(i1, fp) || cf.insertIntoBucket(i2, fp) {
		return true
	}

	// 需要进行 Kick 操作
	index := i1
	for n := 0; n < MaxKicks; n++ {
		entryIdx := rand.Intn(BucketSize) // 随机选择一个槽位进行踢出
		evictedFp := cf.buckets[index].entries[entryIdx]
		cf.buckets[index].entries[entryIdx] = fp

		// 使用当前 bucket 索引和被踢出项混合后重新计算新的索引
		index = (index ^ int(((evictedFp>>4)&0xFF)^(evictedFp&0xFF))) % cf.size
		if cf.insertIntoBucket(index, evictedFp) {
			return true
		}
		fp = evictedFp // 继续踢出流程
	}

	return false // 插入失败
}

// insertIntoBucket 尝试将 vaddr 插入到指定 bucket 中
func (cf *CuckooFilter) insertIntoBucket(index int, vaddr uint64) bool {
	pageVaddr := vaddr
	for i := 0; i < BucketSize; i++ {
		if cf.buckets[index].entries[i] == 0 { // 找到空位
			cf.buckets[index].entries[i] = pageVaddr
			return true
		}
	}
	return false
}

// Lookup 查询 vaddr 是否存在于 Cuckoo Filter 中
func (cf *CuckooFilter) Lookup(vaddr uint64) bool {
	pageVaddr := cf.alignToPage(vaddr)
	fp, i1, i2 := cf.hash(pageVaddr)

	// 在两个可能的 bucket 中查找
	for i := 0; i < BucketSize; i++ {
		if cf.buckets[i1].entries[i] == fp || cf.buckets[i2].entries[i] == fp {
			// fmt.Printf("found %d\n", vaddr)
			return true
		}
	}
	return false
}

// Delete 删除一个 vaddr
func (cf *CuckooFilter) Delete(vaddr uint64) bool {
	pageVaddr := vaddr
	fp, i1, i2 := cf.hash(pageVaddr)
	// 尝试从两个 bucket 中删除
	if cf.deleteFromBucket(i1, fp) || cf.deleteFromBucket(i2, fp) {
		return true
	}
	return false
}

// deleteFromBucket 尝试从指定 bucket 删除一个 vaddr
func (cf *CuckooFilter) deleteFromBucket(index int, vaddr uint64) bool {
	pageVaddr := cf.alignToPage(vaddr)
	for i := 0; i < BucketSize; i++ {
		if cf.buckets[index].entries[i] == pageVaddr {
			cf.buckets[index].entries[i] = 0 // 删除该项
			return true
		}
	}
	return false
}

// alignToPage 将地址对齐到页（右移 12 位）
func (cf *CuckooFilter) alignToPage(addr uint64) uint64 {
	return (addr >> 12)
}

// IsFull 判断 Cuckoo Filter 是否已满
func (cf *CuckooFilter) IsFull() bool {
	for _, bucket := range cf.buckets {
		for _, entry := range bucket.entries {
			if entry == 0 {
				return false // 存在空位，未满
			}
		}
	}
	return true // 无空位，已满
}

// package internal

// import (
// 	"fmt"
// 	"math/rand"
// 	"time"
// )

// // 配置 Cuckoo Filter
// const (
// 	BucketSize = 4   // 每个 bucket 存储的 vaddr 数量
// 	MaxKicks   = 500 // 允许的最大 kick 次数
// )

// // Bucket 结构体，存储 vaddr（即 fingerprint）
// type Bucket struct {
// 	entries [BucketSize]uint64
// }

// // CuckooFilter 结构体
// type CuckooFilter struct {
// 	buckets []Bucket
// 	size    int
// 	name    string
// }

// // NewCuckooFilter 创建一个 Cuckoo Filter
// func NewCuckooFilter(capacity int, name string) *CuckooFilter {
// 	rand.Seed(time.Now().UnixNano())
// 	return &CuckooFilter{
// 		buckets: make([]Bucket, capacity),
// 		size:    capacity,
// 		name:    name,
// 	}
// }

// // hash 计算 page vaddr 对应的两个哈希索引
// func (cf *CuckooFilter) hash(vaddr uint64) (uint64, int, int) {
// 	index1 := int(vaddr % uint64(cf.size)) // 第一个索引
// 	// index2 := int((index1 ^ int(vaddr>>32)) % cf.size) // 第二个索引（Cuckoo Hashing）
// 	index2 := int((index1 ^ int((vaddr*2654435761)>>16)) % cf.size)
// 	return vaddr, index1, index2
// }

// // Insert 插入一个 vaddr
// func (cf *CuckooFilter) Insert(vaddr uint64) bool {
// 	pageVaddr := cf.alignToPage(vaddr)
// 	// pageVaddr := vaddr
// 	fp, i1, i2 := cf.hash(pageVaddr)

// 	// 尝试插入到 i1 位置的 bucket
// 	if cf.insertIntoBucket(i1, fp) || cf.insertIntoBucket(i2, fp) {
// 		return true
// 	}

// 	// 需要进行 Kick
// 	index := i1
// 	for n := 0; n < MaxKicks; n++ {
// 		entryIdx := rand.Intn(BucketSize) // 选择一个要踢出的 vaddr
// 		evictedFp := cf.buckets[index].entries[entryIdx]
// 		cf.buckets[index].entries[entryIdx] = fp

// 		// index = (index ^ int(evictedFp>>8)) % cf.size // 计算新的索引
// 		index = (index ^ int(((evictedFp>>4)&0xFF)^(evictedFp&0xFF))) % cf.size
// 		if cf.insertIntoBucket(index, evictedFp) {
// 			return true
// 		}
// 		fp = evictedFp // 继续 kick
// 	}

// 	return false // 插入失败
// }

// // insertIntoBucket 尝试将 vaddr 插入到指定 bucket
// func (cf *CuckooFilter) insertIntoBucket(index int, vaddr uint64) bool {
// 	// pageVaddr := cf.alignToPage(vaddr)
// 	pageVaddr := vaddr
// 	// fmt.Printf("insert Page Vaddr: %016x\n", pageVaddr)
// 	for i := 0; i < BucketSize; i++ {
// 		if cf.buckets[index].entries[i] == 0 { // 空位
// 			cf.buckets[index].entries[i] = pageVaddr
// 			return true
// 		}
// 	}
// 	return false
// }

// // Lookup 查询 vaddr 是否存在
// func (cf *CuckooFilter) Lookup(vaddr uint64) bool {
// 	pageVaddr := cf.alignToPage(vaddr)
// 	// pageVaddr := vaddr
// 	fp, i1, i2 := cf.hash(pageVaddr)

// 	// 在两个 bucket 里查找
// 	// fmt.Printf("find Page Vaddr:%016x\n", pageVaddr)
// 	for i := 0; i < BucketSize; i++ {
// 		if cf.buckets[i1].entries[i] == fp || cf.buckets[i2].entries[i] == fp {
// 			fmt.Printf("found %d\n", vaddr)
// 			return true
// 		}
// 	}
// 	// fmt.Printf("not found %d\n", vaddr)
// 	return false
// }

// // Delete 删除一个 vaddr
// func (cf *CuckooFilter) Delete(vaddr uint64) bool {
// 	// pageVaddr := cf.alignToPage(vaddr)

// 	pageVaddr := vaddr
// 	fp, i1, i2 := cf.hash(pageVaddr)
// 	// 尝试删除
// 	if cf.deleteFromBucket(i1, fp) || cf.deleteFromBucket(i2, fp) {
// 		return true
// 	}
// 	return false
// }

// // deleteFromBucket 尝试从 bucket 删除一个 vaddr
// func (cf *CuckooFilter) deleteFromBucket(index int, vaddr uint64) bool {
// 	pageVaddr := cf.alignToPage(vaddr)
// 	// pageVaddr := vaddr

// 	for i := 0; i < BucketSize; i++ {
// 		if cf.buckets[index].entries[i] == pageVaddr {
// 			cf.buckets[index].entries[i] = 0 // 删除
// 			return true
// 		}
// 	}
// 	return false
// }

// func (cf *CuckooFilter) alignToPage(addr uint64) uint64 {
// 	return (addr >> 12)
// }

// func (cf *CuckooFilter) IsFull() bool {
// 	for _, bucket := range cf.buckets {
// 		for _, entry := range bucket.entries {
// 			if entry == 0 {
// 				return false // 发现空位，说明没满
// 			}
// 		}
// 	}
// 	return true // 没有发现空位，说明已满
// }
