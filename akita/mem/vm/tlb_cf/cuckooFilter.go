package tlb_cf

import (
	"errors"
	"hash/fnv"

	"golang.org/x/exp/rand"
)

const (
	bucketSize      = 4   // 每个桶最多存储的 fingerprints
	numBuckets      = 256 // 总桶的数量
	maxRelocate     = 500 // 插入时的最大重定位次数
	fingerprintSize = 8   // 指纹的大小（字节数）
)

type CuckooFilter struct {
	buckets [][]uint64 // 存储指纹的二维数组
}

func NewCuckooFilter() *CuckooFilter {
	// 初始化桶
	buckets := make([][]uint64, numBuckets)
	for i := range buckets {
		buckets[i] = make([]uint64, 0, bucketSize)
	}
	return &CuckooFilter{buckets: buckets}
}

// hash 计算给定数据的哈希值
func hash(data uint64) uint64 {
	h := fnv.New64()
	_, _ = h.Write([]byte{byte(data), byte(data >> 8), byte(data >> 16), byte(data >> 24)})
	return h.Sum64()
}

// fingerprint 生成一个元素的指纹
func fingerprint(data uint64) uint64 {
	fp := hash(data) & ((1 << (fingerprintSize * 8)) - 1)
	if fp == 0 {
		fp = 1 // 确保指纹不为0
	}
	return fp
}

// getBucketIndices 获取元素所在的两个桶的索引
func getBucketIndices(fp, h uint64) (uint64, uint64) {
	i1 := h % numBuckets
	i2 := (i1 ^ (fp % numBuckets)) % numBuckets
	return i1, i2
}

// Lookup 查找一个元素是否在过滤器中
func (cf *CuckooFilter) Lookup(data uint64) bool {
	fp := fingerprint(data)
	h := hash(data)
	i1, i2 := getBucketIndices(fp, h)

	// 检查两个桶是否包含指纹
	return cf.bucketContains(i1, fp) || cf.bucketContains(i2, fp)
}

// bucketContains 检查桶中是否包含指定指纹
func (cf *CuckooFilter) bucketContains(bucketIndex uint64, fp uint64) bool {
	for _, v := range cf.buckets[bucketIndex] {
		if v == fp {
			return true
		}
	}
	return false
}

// Delete 删除一个元素
func (cf *CuckooFilter) Delete(data uint64) bool {
	fp := fingerprint(data)
	h := hash(data)
	i1, i2 := getBucketIndices(fp, h)

	// 尝试从两个桶中删除
	if cf.removeFromBucket(i1, fp) || cf.removeFromBucket(i2, fp) {
		return true
	}
	return false
}

// removeFromBucket 从桶中移除指定指纹
func (cf *CuckooFilter) removeFromBucket(bucketIndex uint64, fp uint64) bool {
	for i, v := range cf.buckets[bucketIndex] {
		if v == fp {
			// 删除该指纹
			cf.buckets[bucketIndex] = append(cf.buckets[bucketIndex][:i], cf.buckets[bucketIndex][i+1:]...)
			return true
		}
	}
	return false
}

func (cf *CuckooFilter) Insert(data uint64) error {
	fp := fingerprint(data)
	h := hash(data)
	i1, i2 := getBucketIndices(fp, h)

	// 尝试插入到两个桶之一
	if cf.insertIntoBucket(i1, fp) || cf.insertIntoBucket(i2, fp) {
		return nil
	}

	// 桶满：从第一个桶删除最后一个元素，尝试将其踢到另一个桶
	removedFp := cf.evictLastElement(i1)
	if cf.insertIntoBucket(i2, removedFp) {
		return nil
	}

	// 如果第二个桶也满，再删除第二个桶的最后一个元素并尝试重新插入
	removedFp2 := cf.evictLastElement(i2)
	if cf.insertIntoBucket(i1, removedFp2) {
		return nil
	}

	// 如果还是失败，随机替换一个已有的元素
	if cf.replaceRandom(i1, i2, fp) {
		return nil
	}

	return errors.New("filter is full, failed to insert")
}

// replaceRandom 随机替换两个桶中的一个元素
func (cf *CuckooFilter) replaceRandom(bucketIndex1, bucketIndex2 uint64, newFp uint64) bool {
	// 从两个桶中随机选择一个桶
	selectedBucketIndex := bucketIndex1
	if rand.Intn(2) == 1 {
		selectedBucketIndex = bucketIndex2
	}

	// 随机选择该桶中的一个元素进行替换
	selectedBucket := cf.buckets[selectedBucketIndex]
	if len(selectedBucket) > 0 {
		r := rand.Intn(len(selectedBucket))
		selectedBucket[r] = newFp
		return true
	}
	return false
}

// evictLastElement 删除桶中的最后一个元素并返回它
func (cf *CuckooFilter) evictLastElement(bucketIndex uint64) uint64 {
	bucket := cf.buckets[bucketIndex]
	removedFp := bucket[len(bucket)-1]               // 获取最后一个元素
	cf.buckets[bucketIndex] = bucket[:len(bucket)-1] // 删除最后一个元素
	return removedFp
}

// insertIntoBucket 尝试将指纹插入到指定桶中
func (cf *CuckooFilter) insertIntoBucket(bucketIndex uint64, fp uint64) bool {
	if len(cf.buckets[bucketIndex]) < bucketSize {
		cf.buckets[bucketIndex] = append(cf.buckets[bucketIndex], fp)
		return true
	}
	return false
}
