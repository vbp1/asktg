package vector

import (
	"encoding/gob"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	hnswlib "github.com/Bithack/go-hnsw"
)

type Item struct {
	ChunkID int64
	Vector  []float32
}

type Candidate struct {
	ChunkID  int64
	Distance float64
	Internal uint32
	RawRank  int
}

type HNSW struct {
	mu             sync.RWMutex
	dimensions     int
	m              int
	efConstruction int
	efSearch       int

	index      *hnswlib.Hnsw
	toInternal map[int64]uint32
	fromChunk  map[uint32]int64
	tombstones map[uint32]struct{}
	nextID     uint32
}

type persistedSnapshot struct {
	Dimensions     int
	M              int
	EfConstruction int
	EfSearch       int
	ToInternal     map[int64]uint32
	FromChunk      map[uint32]int64
	Tombstones     map[uint32]struct{}
	NextID         uint32
	GraphBytes     []byte
}

func NewHNSW(dimensions, m, efConstruction, efSearch int) *HNSW {
	if dimensions <= 0 {
		dimensions = 3072
	}
	if m <= 0 {
		m = 16
	}
	if efConstruction <= 0 {
		efConstruction = 200
	}
	if efSearch <= 0 {
		efSearch = 64
	}
	return &HNSW{
		dimensions:     dimensions,
		m:              m,
		efConstruction: efConstruction,
		efSearch:       efSearch,
		toInternal:     map[int64]uint32{},
		fromChunk:      map[uint32]int64{},
		tombstones:     map[uint32]struct{}{},
		nextID:         1,
	}
}

func (h *HNSW) Dimensions() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.dimensions
}

func (h *HNSW) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.toInternal)
}

func (h *HNSW) Add(chunkID int64, vector []float32) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addLocked(chunkID, vector)
}

func (h *HNSW) addLocked(chunkID int64, vector []float32) error {
	if chunkID == 0 {
		return errors.New("chunk id must be non-zero")
	}
	if len(vector) == 0 {
		return errors.New("vector is required")
	}
	if h.dimensions <= 0 {
		h.dimensions = len(vector)
	}
	if len(vector) != h.dimensions {
		return errors.New("vector dimensions mismatch")
	}

	if existing, ok := h.toInternal[chunkID]; ok {
		h.tombstones[existing] = struct{}{}
		delete(h.toInternal, chunkID)
	}

	if h.index == nil {
		base := make(hnswlib.Point, h.dimensions)
		h.index = hnswlib.New(h.m, h.efConstruction, base)
		h.index.DistFunc = l2Distance
		h.nextID = 1
	}
	if h.nextID == math.MaxUint32 {
		return errors.New("vector internal id overflow")
	}
	internalID := h.nextID
	h.nextID++
	if internalID == 0 {
		return errors.New("vector internal id overflow")
	}

	h.index.Grow(int(internalID) + 1)
	h.index.Add(hnswlib.Point(vector), internalID)
	h.toInternal[chunkID] = internalID
	h.fromChunk[internalID] = chunkID
	delete(h.tombstones, internalID)
	return nil
}

func (h *HNSW) MarkDeleted(chunkID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	internal, ok := h.toInternal[chunkID]
	if !ok {
		return
	}
	h.tombstones[internal] = struct{}{}
	delete(h.toInternal, chunkID)
}

func (h *HNSW) Search(vector []float32, limit int) []Candidate {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.index == nil || len(vector) == 0 || len(vector) != h.dimensions || limit <= 0 {
		return nil
	}

	ef := h.efSearch
	if ef < limit*8 {
		ef = limit * 8
	}
	raw := h.index.Search(hnswlib.Point(vector), ef, ef)
	items := raw.Items()
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].D < items[j].D })

	results := make([]Candidate, 0, limit)
	seen := make(map[int64]struct{}, limit*2)
	for idx, item := range items {
		if item == nil || item.ID == 0 {
			continue
		}
		if _, tombstoned := h.tombstones[item.ID]; tombstoned {
			continue
		}
		chunkID, ok := h.fromChunk[item.ID]
		if !ok || chunkID == 0 {
			continue
		}
		latest, ok := h.toInternal[chunkID]
		if !ok || latest != item.ID {
			continue
		}
		if _, exists := seen[chunkID]; exists {
			continue
		}
		seen[chunkID] = struct{}{}
		results = append(results, Candidate{
			ChunkID:  chunkID,
			Distance: float64(item.D),
			Internal: item.ID,
			RawRank:  idx + 1,
		})
		if len(results) >= limit {
			break
		}
	}
	return results
}

func (h *HNSW) Rebuild(items []Item) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.index = nil
	h.toInternal = map[int64]uint32{}
	h.fromChunk = map[uint32]int64{}
	h.tombstones = map[uint32]struct{}{}
	h.nextID = 1

	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ChunkID < items[j].ChunkID })
	for _, item := range items {
		if err := h.addLocked(item.ChunkID, item.Vector); err != nil {
			return err
		}
	}
	return nil
}

func (h *HNSW) Save(path string) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if stringsTrim(path) == "" {
		return errors.New("path is required")
	}
	clean := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return err
	}

	snapshot := persistedSnapshot{
		Dimensions:     h.dimensions,
		M:              h.m,
		EfConstruction: h.efConstruction,
		EfSearch:       h.efSearch,
		ToInternal:     copyChunkMap(h.toInternal),
		FromChunk:      copyInternalMap(h.fromChunk),
		Tombstones:     copyTombstones(h.tombstones),
		NextID:         h.nextID,
	}

	if h.index != nil {
		tempDir, err := os.MkdirTemp("", "asktg-hnsw-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tempDir)

		graphPath := filepath.Join(tempDir, "graph.bin")
		if err := h.index.Save(graphPath); err != nil {
			return err
		}
		graphBytes, err := os.ReadFile(filepath.Clean(graphPath))
		if err != nil {
			return err
		}
		snapshot.GraphBytes = graphBytes
	}

	file, err := os.Create(clean)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := gob.NewEncoder(file)
	return encoder.Encode(snapshot)
}

func (h *HNSW) Load(path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	clean := filepath.Clean(path)
	file, err := os.Open(clean)
	if err != nil {
		return err
	}
	defer file.Close()

	var snapshot persistedSnapshot
	decoder := gob.NewDecoder(file)
	if err := decoder.Decode(&snapshot); err != nil {
		return err
	}

	if snapshot.Dimensions <= 0 {
		return errors.New("invalid snapshot dimensions")
	}

	h.dimensions = snapshot.Dimensions
	h.m = snapshot.M
	h.efConstruction = snapshot.EfConstruction
	h.efSearch = snapshot.EfSearch
	if h.m <= 0 {
		h.m = 16
	}
	if h.efConstruction <= 0 {
		h.efConstruction = 200
	}
	if h.efSearch <= 0 {
		h.efSearch = 64
	}
	h.toInternal = copyChunkMap(snapshot.ToInternal)
	h.fromChunk = copyInternalMap(snapshot.FromChunk)
	h.tombstones = copyTombstones(snapshot.Tombstones)
	h.nextID = snapshot.NextID
	if h.nextID == 0 {
		h.nextID = 1
	}

	h.index = nil
	if len(snapshot.GraphBytes) == 0 {
		return nil
	}
	tempDir, err := os.MkdirTemp("", "asktg-hnsw-load-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	graphPath := filepath.Join(tempDir, "graph.bin")
	if err := os.WriteFile(graphPath, snapshot.GraphBytes, 0o644); err != nil {
		return err
	}
	index, _, err := hnswlib.Load(graphPath)
	if err != nil {
		return err
	}
	index.DistFunc = l2Distance
	h.index = index
	return nil
}

func copyChunkMap(in map[int64]uint32) map[int64]uint32 {
	out := make(map[int64]uint32, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyInternalMap(in map[uint32]int64) map[uint32]int64 {
	out := make(map[uint32]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyTombstones(in map[uint32]struct{}) map[uint32]struct{} {
	out := make(map[uint32]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func stringsTrim(value string) string {
	start := 0
	end := len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	if start >= end {
		return ""
	}
	return value[start:end]
}

func l2Distance(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return float32(math.MaxFloat32)
	}
	var sum float32
	for idx := range a {
		diff := a[idx] - b[idx]
		sum += diff * diff
	}
	return sum
}
