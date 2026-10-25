package structure_test

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/structure"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

// TestMain prepares Dragonfly's block registry before structures use it.
func TestMain(m *testing.M) {
	world.DefaultBlockRegistry.Finalize()
	os.Exit(m.Run())
}

func TestReadUncompressed(t *testing.T) {
	for _, version := range []int32{1, 2} {
		for _, explicitCompression := range []bool{false, true} {
			t.Run(fmt.Sprintf("version_%d/compression_tag_%t", version, explicitCompression), func(t *testing.T) {
				t.Parallel()
				want := newTestStructure(true)
				disk := encodedStructure(t, want)
				disk["format_version"] = version
				if explicitCompression {
					disk["compression"] = int32(0)
				} else {
					delete(disk, "compression")
				}
				if version == 1 {
					// Version 1 uses lists instead of the int arrays written by version 2.
					layers := make([]any, len(want.Structure.BlockIndices))
					for i, layer := range want.Structure.BlockIndices {
						layers[i] = layer
					}
					disk["structure"].(map[string]any)["block_indices"] = layers
				}
				got, err := structure.Read(bytes.NewReader(marshalStructure(t, disk)))
				if err != nil {
					t.Fatal(err)
				}
				if got.FormatVersion != version || got.Compression != 0 {
					t.Errorf("got version %d, compression %d; want version %d, compression 0", got.FormatVersion, got.Compression, version)
				}
				assertStructure(t, got, want)
			})
		}
	}
}

func TestCompressionRoundTrip(t *testing.T) {
	for _, compression := range []int32{0, 1} {
		for _, liquids := range []bool{false, true} {
			t.Run(fmt.Sprintf("compression_%d/liquids_%t", compression, liquids), func(t *testing.T) {
				t.Parallel()
				want := newTestStructure(liquids)
				want.Compression = compression
				disk := encodedStructure(t, want)
				if disk["format_version"] != int32(2) {
					t.Fatalf("got format version %v, want 2", disk["format_version"])
				}
				var data map[string]any
				if compression == 1 {
					if disk["compression"] != int32(1) {
						t.Fatalf("got compression %v, want 1", disk["compression"])
					}
					value := reflect.ValueOf(disk["structure"])
					if value.Kind() != reflect.Array || value.Type().Elem().Kind() != reflect.Uint8 {
						t.Fatalf("compressed structure has type %T, want a byte array", disk["structure"])
					}
					compressed := make([]byte, value.Len())
					reflect.Copy(reflect.ValueOf(compressed), value)
					r, err := zlib.NewReader(bytes.NewReader(compressed))
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					decoded, err := io.ReadAll(r)
					if err != nil {
						t.Fatal(err)
					}
					if err := nbt.UnmarshalEncoding(decoded, &data, nbt.LittleEndian); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, ok := disk["compression"]; ok {
						t.Fatal("uncompressed output should omit the compression tag")
					}
					data = disk["structure"].(map[string]any)
				}
				layerCount := 1
				if liquids {
					layerCount = 2
				}
				if got := len(data["block_indices"].([]any)); got != layerCount {
					t.Errorf("got %d block layers, want %d", got, layerCount)
				}
				got, err := structure.Read(bytes.NewReader(marshalStructure(t, disk)))
				if err != nil {
					t.Fatal(err)
				}
				if got.Compression != compression {
					t.Errorf("got compression %d, want %d", got.Compression, compression)
				}
				assertStructure(t, got, want)
			})
		}
	}
}

func TestReadInvalidCompression(t *testing.T) {
	for _, test := range []struct {
		name        string
		version     int32
		compression int32
		data        any
		wantError   string
	}{
		{"negative", 2, -1, nil, "unsupported compression"},
		{"unknown", 2, 2, nil, "unsupported compression"},
		{"version_1", 1, 1, nil, "unsupported compression"},
		{"compressed_compound", 2, 1, nil, "TAG_ByteArray"},
		{"uncompressed_byte_array", 2, 0, [1]byte{}, "TAG_Compound"},
		{"invalid_zlib", 2, 1, [3]byte{1, 2, 3}, "decompress"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			disk := encodedStructure(t, newTestStructure(false))
			disk["format_version"] = test.version
			disk["compression"] = test.compression
			if test.data != nil {
				disk["structure"] = test.data
			}
			_, err := structure.Read(bytes.NewReader(marshalStructure(t, disk)))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("got error %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestReadInvalidChecksum(t *testing.T) {
	s := newTestStructure(false)
	s.Compression = 1
	disk := encodedStructure(t, s)
	value := reflect.ValueOf(disk["structure"])
	corrupt := reflect.New(value.Type()).Elem()
	corrupt.Set(value)
	last := corrupt.Index(corrupt.Len() - 1)
	last.SetUint(last.Uint() ^ 1)
	disk["structure"] = corrupt.Interface()
	_, err := structure.Read(bytes.NewReader(marshalStructure(t, disk)))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("got error %v, want a checksum error", err)
	}
}

func TestWriteInvalidCompression(t *testing.T) {
	for _, compression := range []int32{-1, 2} {
		t.Run(fmt.Sprint(compression), func(t *testing.T) {
			s := newTestStructure(false)
			s.Compression = compression
			var buf bytes.Buffer
			if err := structure.Write(&buf, s); err == nil || !strings.Contains(err.Error(), "unsupported compression") {
				t.Fatalf("got error %v, want unsupported compression", err)
			}
			if buf.Len() != 0 {
				t.Fatal("invalid compression wrote output")
			}
		})
	}
}

// newTestStructure creates a small structure with blocks, metadata, and optional water.
func newTestStructure(liquids bool) structure.Structure {
	s := structure.New([3]int{2, 1, 1})
	s.Origin = []int32{-8, 64, 16}
	s.Set(0, 0, 0, block.Stone{}, nil)
	if liquids {
		s.Set(1, 0, 0, block.Air{}, block.Water{Still: true, Depth: 8})
	}
	s.Structure.Entities = []map[string]any{{
		"identifier": "minecraft:armor_stand",
		"Pos":        []any{float32(0.5), float32(0), float32(0.5)},
		"test_data": map[string]any{
			"bytes": [3]byte{1, 2, 3},
			"ints":  [2]int32{4, 5},
			"longs": [2]int64{6, 7},
		},
	}}
	return s
}

// encodedStructure writes a structure and exposes its root NBT tags for format checks.
func encodedStructure(t *testing.T, s structure.Structure) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := structure.Write(&buf, s); err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := nbt.UnmarshalEncoding(buf.Bytes(), &disk, nbt.LittleEndian); err != nil {
		t.Fatal(err)
	}
	return disk
}

// marshalStructure encodes root NBT tags as an mcstructure file.
func marshalStructure(t *testing.T, disk map[string]any) []byte {
	t.Helper()
	b, err := nbt.MarshalEncoding(disk, nbt.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertStructure checks that loading preserves metadata, entities, blocks, and liquids.
func assertStructure(t *testing.T, got, want structure.Structure) {
	t.Helper()
	if got.Dimensions() != want.Dimensions() || !reflect.DeepEqual(got.Origin, want.Origin) {
		t.Errorf("got dimensions %v, origin %v; want %v, %v", got.Dimensions(), got.Origin, want.Dimensions(), want.Origin)
	}
	if !reflect.DeepEqual(got.Structure.Entities, want.Structure.Entities) {
		t.Errorf("entities changed: got %#v, want %#v", got.Structure.Entities, want.Structure.Entities)
	}
	if !reflect.DeepEqual(got.Structure.BlockIndices, want.Structure.BlockIndices) {
		t.Errorf("block indices changed: got %v, want %v", got.Structure.BlockIndices, want.Structure.BlockIndices)
	}
	for x := range want.Dimensions()[0] {
		gotBlock, gotLiquid := got.At(x, 0, 0, nil)
		wantBlock, wantLiquid := want.At(x, 0, 0, nil)
		if !reflect.DeepEqual(gotBlock, wantBlock) || !reflect.DeepEqual(gotLiquid, wantLiquid) {
			t.Errorf("position %d: got %v, %v; want %v, %v", x, gotBlock, gotLiquid, wantBlock, wantLiquid)
		}
	}
}
