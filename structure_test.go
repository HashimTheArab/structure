package structure

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

func TestWriteUsesVersionTwoIntArrays(t *testing.T) {
	s := New([3]int{1, 1, 1})

	var encoded bytes.Buffer
	if err := Write(&encoded, s); err != nil {
		t.Fatalf("write structure: %v", err)
	}

	var root map[string]interface{}
	if err := nbt.NewDecoderWithEncoding(&encoded, nbt.LittleEndian).Decode(&root); err != nil {
		t.Fatalf("decode written structure: %v", err)
	}
	if got := root["format_version"]; got != int32(2) {
		t.Fatalf("format version = %v, want 2", got)
	}

	data := root["structure"].(map[string]interface{})
	indices := data["block_indices"].([]interface{})
	if len(indices) != 1 {
		t.Fatalf("block index layer count = %v, want 1", len(indices))
	}
	if got := reflect.TypeOf(indices[0]).Kind(); got != reflect.Array {
		t.Fatalf("block index layer kind = %v, want array for TAG_IntArray", got)
	}
}

func TestWriteIncludesNonEmptyExtraBlockLayer(t *testing.T) {
	s := New([3]int{1, 1, 1})
	s.Structure.BlockIndices[1][0] = 0

	var encoded bytes.Buffer
	if err := Write(&encoded, s); err != nil {
		t.Fatalf("write structure: %v", err)
	}

	var root map[string]interface{}
	if err := nbt.NewDecoderWithEncoding(&encoded, nbt.LittleEndian).Decode(&root); err != nil {
		t.Fatalf("decode written structure: %v", err)
	}
	data := root["structure"].(map[string]interface{})
	if got := len(data["block_indices"].([]interface{})); got != 2 {
		t.Fatalf("block index layer count = %v, want 2", got)
	}
}

func TestReadVersionOneBlockIndexLists(t *testing.T) {
	legacy := &structure{
		FormatVersion: 1,
		Size:          []int32{1, 1, 1},
		Origin:        []int32{0, 0, 0},
		Structure: structureData{
			BlockIndices: [][]int32{{0}, {-1}},
			Palettes: map[string]palette{"default": {
				BlockPalette: []block{{
					Name:    "minecraft:air",
					States:  map[string]interface{}{},
					Version: 1,
				}},
				BlockPositionData: map[string]blockPositionData{},
			}},
		},
	}

	var encoded bytes.Buffer
	if err := nbt.NewEncoderWithEncoding(&encoded, nbt.LittleEndian).Encode(legacy); err != nil {
		t.Fatalf("encode version 1 structure: %v", err)
	}
	if _, err := Read(&encoded); err != nil {
		t.Fatalf("read version 1 structure: %v", err)
	}
}

func TestReadVersionTwoIntArrayWithOneLayer(t *testing.T) {
	front := reflect.New(reflect.ArrayOf(1, reflect.TypeOf(int32(0)))).Elem()
	front.Index(0).SetInt(0)
	root := map[string]interface{}{
		"format_version":         int32(2),
		"size":                   []int32{1, 1, 1},
		"structure_world_origin": []int32{0, 0, 0},
		"structure": map[string]interface{}{
			"block_indices": []interface{}{front.Interface()},
			"entities":      []map[string]interface{}{},
			"palette": map[string]palette{"default": {
				BlockPalette: []block{{
					Name:    "minecraft:air",
					States:  map[string]interface{}{},
					Version: 1,
				}},
				BlockPositionData: map[string]blockPositionData{},
			}},
		},
	}

	var encoded bytes.Buffer
	if err := nbt.NewEncoderWithEncoding(&encoded, nbt.LittleEndian).Encode(root); err != nil {
		t.Fatalf("encode version 2 structure: %v", err)
	}
	s, err := Read(&encoded)
	if err != nil {
		t.Fatalf("read version 2 structure: %v", err)
	}
	if got := len(s.Structure.BlockIndices); got != 2 {
		t.Fatalf("prepared block index layer count = %v, want 2", got)
	}
}

func TestReadRejectsVersionTwoBlockIndexLists(t *testing.T) {
	legacy := &structure{
		FormatVersion: 2,
		Size:          []int32{1, 1, 1},
		Origin:        []int32{0, 0, 0},
		Structure: structureData{
			BlockIndices: [][]int32{{0}},
			Palettes: map[string]palette{"default": {
				BlockPalette: []block{{
					Name:    "minecraft:air",
					States:  map[string]interface{}{},
					Version: 1,
				}},
				BlockPositionData: map[string]blockPositionData{},
			}},
		},
	}

	var encoded bytes.Buffer
	if err := nbt.NewEncoderWithEncoding(&encoded, nbt.LittleEndian).Encode(legacy); err != nil {
		t.Fatalf("encode invalid version 2 structure: %v", err)
	}
	_, err := Read(&encoded)
	if err == nil || !strings.Contains(err.Error(), "TAG_IntArray") {
		t.Fatalf("read error = %v, want TAG_IntArray error", err)
	}
}
