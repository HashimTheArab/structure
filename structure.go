package structure

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"fmt"
	"go/ast"
	"io"
	"os"
	"reflect"

	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

// Structure holds the data of an .mcstructure file. Structure implements the world.Structure interface. It
// may be built in a Dragonfly world by using (world.World).BuildStructure.
// Users must ensure Structure is only accessed from one goroutine at a time.
type Structure struct {
	*structure
}

type diskStructure struct {
	FormatVersion int32   `nbt:"format_version"`
	Compression   int32   `nbt:"compression,omitempty"`
	Size          []int32 `nbt:"size"`
	Origin        []int32 `nbt:"structure_world_origin"`
	Structure     any     `nbt:"structure"`
}

type diskStructureData struct {
	BlockIndices []any              `nbt:"block_indices"`
	Entities     []map[string]any   `nbt:"entities"`
	Palettes     map[string]palette `nbt:"palette"`
}

// decompress decompresses the given data using zlib. It is used for structures
// with the compression tag set to 1.
func decompress(b []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("create decompressor: %w", err)
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Read attempts to read a Structure from the io.Reader passed. If successful, the Structure returned is
// valid and the error is nil.
// Read uses a palette name of 'default' by default. UsePalette may be used to change the name of the
// palette to use.
func Read(r io.Reader) (Structure, error) {
	disk := &diskStructure{}
	if err := nbt.NewDecoderWithEncoding(r, nbt.LittleEndian).Decode(disk); err != nil {
		return Structure{}, fmt.Errorf("decode structure: %w", err)
	}
	var b []byte
	var err error
	if disk.Compression != 0 {
		if disk.FormatVersion != 2 || disk.Compression != 1 {
			return Structure{}, fmt.Errorf("unsupported compression %d for format version %d", disk.Compression, disk.FormatVersion)
		}
		compressed, ok := byteArray(disk.Structure)
		if !ok {
			return Structure{}, fmt.Errorf("structure tag must be a TAG_ByteArray, got %T", disk.Structure)
		}
		b, err = decompress(compressed)
		if err != nil {
			return Structure{}, fmt.Errorf("decompress: %w", err)
		}
	} else {
		if _, ok := disk.Structure.(map[string]any); !ok {
			return Structure{}, fmt.Errorf("expected structure to be TAG_Compound, got %T", disk.Structure)
		}
		// NBT has no map-to-struct conversion, so reuse its encoder and decoder.
		b, err = nbt.MarshalEncoding(disk.Structure, nbt.LittleEndian)
		if err != nil {
			return Structure{}, fmt.Errorf("encode structure: %w", err)
		}
	}
	var data diskStructureData
	if err := nbt.UnmarshalEncoding(b, &data, nbt.LittleEndian); err != nil {
		return Structure{}, fmt.Errorf("decode structure: %w", err)
	}

	indices, err := decodeBlockIndices(data.BlockIndices, disk.FormatVersion)
	if err != nil {
		return Structure{}, fmt.Errorf("decode structure: %w", err)
	}
	s := &structure{
		FormatVersion: disk.FormatVersion,
		Size:          disk.Size,
		Origin:        disk.Origin,
		Compression:   disk.Compression,
		Structure: structureData{
			BlockIndices: indices,
			Entities:     data.Entities,
			Palettes:     data.Palettes,
		},
	}
	if err := s.check(); err != nil {
		return Structure{}, fmt.Errorf("verify structure: %w", err)
	}
	str := Structure{structure: s}
	str.UsePalette("default")
	str.prepare()
	return str, nil
}

// byteArray attempts to convert the given byte array to a byte slice.
func byteArray(value any) ([]byte, bool) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Kind() != reflect.Array || v.Type().Elem().Kind() != reflect.Uint8 {
		return nil, false
	}
	b := make([]byte, v.Len())
	reflect.Copy(reflect.ValueOf(b), v)
	return b, true
}

// ReadFile attempts to read a Structure from a file at the path passed. If successful, the error returned is
// nil.
// ReadFile, like Read, uses a palette name of 'default' by default. UsePalette may be used to change
// the name of the palette to use.
func ReadFile(file string) (Structure, error) {
	f, err := os.Open(file)
	if err != nil {
		return Structure{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	return Read(bufio.NewReader(f))
}

// Write writes a Structure to the io.Writer passed. If successful, the error returned is nil.
func Write(w io.Writer, s Structure) error {
	s.Structure.Palettes[s.paletteName] = *s.palette
	indices, err := encodeBlockIndices(s.Structure.BlockIndices)
	if err != nil {
		return fmt.Errorf("encode structure: %w", err)
	}
	disk := diskStructure{
		FormatVersion: version,
		Size:          s.Size,
		Origin:        s.Origin,
		Compression:   s.Compression,
		Structure: diskStructureData{
			BlockIndices: indices,
			Entities:     s.Structure.Entities,
			Palettes:     s.Structure.Palettes,
		},
	}
	if disk.Compression != 0 {
		if disk.Compression != 1 {
			return fmt.Errorf("unsupported compression %d for format version %d", disk.Compression, disk.FormatVersion)
		}
		data, err := nbt.MarshalEncoding(disk.Structure, nbt.LittleEndian)
		if err != nil {
			return fmt.Errorf("encode structure: %w", err)
		}
		b, err := compress(data)
		if err != nil {
			return fmt.Errorf("compress: %w", err)
		}
		disk.Structure = makeArray(b)
	}

	if err := nbt.NewEncoderWithEncoding(w, nbt.LittleEndian).Encode(disk); err != nil {
		return fmt.Errorf("encode structure: %w", err)
	}
	return nil
}

// makeArray converts the given slice into an array.
func makeArray[E any](slice []E) any {
	array := reflect.New(reflect.ArrayOf(len(slice), reflect.TypeFor[E]())).Elem()
	reflect.Copy(array, reflect.ValueOf(slice))
	return array.Interface()
}

// compress compresses the given data using zlib. It is used for structures
// with the compression tag set to 1.
func compress(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteFile writes a Structure to the file passed. If successful, the error returned is nil. WriteFile
// creates a file if it doesn't yet exist and truncates it if one does exist.
func WriteFile(file string, s Structure) error {
	f, err := os.OpenFile(file, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	w := bufio.NewWriter(f)
	defer func() {
		_ = w.Flush()
		_ = f.Close()
	}()
	return Write(w, s)
}

// New creates a new Structure and initialises it with air blocks. The Structure returned may be changed using
// Structure.Set, and its palette may be changed using Structure.UsePalette.
func New(dimensions [3]int) Structure {
	front := make([]int32, dimensions[0]*dimensions[1]*dimensions[2])
	liquids := make([]int32, dimensions[0]*dimensions[1]*dimensions[2])
	for i := range liquids {
		liquids[i] = -1
	}

	s := Structure{structure: &structure{
		FormatVersion: version,
		Size:          []int32{int32(dimensions[0]), int32(dimensions[1]), int32(dimensions[2])},
		Origin:        []int32{0, 0, 0},
		Structure: structureData{
			BlockIndices: [][]int32{front, liquids},
			Palettes:     map[string]palette{},
		},
	}}
	s.UsePalette("default")
	s.prepare()
	return s
}

// UsePalette changes the palette name to use for the Structure. When reading a Structure, this will change
// the palette used to read blocks from. When writing a Structure, the palette will be written with this name,
// so that subsequent readers of the Structure must first call UsePalette with this name to get the right
// palette.
func (s Structure) UsePalette(name string) {
	if current := s.palette; current != nil {
		s.Structure.Palettes[s.paletteName] = *s.palette
	}
	p, _ := s.Structure.Palettes[name]
	if p.BlockPositionData == nil {
		p.BlockPositionData = map[string]blockPositionData{}
	}
	s.palette = &p
	s.paletteName = name

	if len(s.palette.BlockPalette) == 0 {
		s.palette.BlockPalette = []block{{
			Name:    "minecraft:air",
			States:  map[string]any{},
			Version: chunk.CurrentBlockVersion,
		}}
	}
	s.parsePalette()
	s.prepare()
}

// RotateLeft returns a new structure with the same contents but rotated 90 degrees anti-clockwise.
func (s Structure) RotateLeft() Structure {
	return s.rotate(-1)
}

// RotateRight returns a new structure with the same contents but rotated 90 degrees clockwise.
func (s Structure) RotateRight() Structure {
	return s.rotate(1)
}

// rotate returns a new structure with the same contents but rotated 90 degrees in the specified direction.
func (s Structure) rotate(direction int) Structure {
	sizeX, sizeY, sizeZ := int(s.Size[0]), int(s.Size[1]), int(s.Size[2])
	newStructure := New([3]int{sizeZ, sizeY, sizeX})

	maxX, maxZ := sizeX-1, sizeZ-1
	for x := 0; x < sizeX; x++ {
		for y := 0; y < sizeY; y++ {
			for z := 0; z < sizeZ; z++ {
				newX, newZ := x, z
				if direction == 1 {
					newX = -z + maxZ
					newZ = x
				} else {
					newX = z
					newZ = -x + maxX
				}
				b, l := s.At(x, y, z, nil)
				newStructure.Set(newX, y, newZ, b, l)
			}
		}
	}
	newStructure.palette = s.palette
	newStructure.palette.BlockPalette = make([]block, len(s.palette.BlockPalette))

	for i, b := range s.parsedPalette {
		origin := reflect.ValueOf(b.b)
		t := reflect.TypeOf(b.b)
		v := reflect.New(t).Elem()

		for i := 0; i < v.NumField(); i++ {
			fieldV := v.Field(i)
			if !ast.IsExported(t.Field(i).Name) {
				continue
			}
			fieldV.Set(origin.Field(i))

			methodName := "RotateLeft"
			if direction == 1 {
				methodName = "RotateRight"
			}
			method := fieldV.MethodByName(methodName)
			if !method.IsZero() {
				fieldV.Set(method.Call(nil)[0])
			}
		}

		name, states := v.Interface().(world.Block).EncodeBlock()
		newStructure.palette.BlockPalette[i] = block{
			Name:    name,
			States:  states,
			Version: chunk.CurrentBlockVersion,
		}
	}
	newStructure.parsePalette()
	newStructure.prepare()
	return newStructure
}
