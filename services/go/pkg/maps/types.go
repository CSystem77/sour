package maps

import (
	"github.com/cfoust/sour/pkg/game"
)

type Header struct {
	Version    int32
	HeaderSize int32
	WorldSize  int32
	LightMaps  int32
	BlendMap   int32
	NumVars    int32
	NumVSlots  int32
	GameType   string
}

func NewHeader() *Header {
	return &Header{
		GameType: "fps",
	}
}

type FileHeader struct {
	Magic      [4]byte
	Version    int32
	HeaderSize int32
	WorldSize  int32
	NumEnts    int32
	NumPVs     int32
	LightMaps  int32
}

type NewFooter struct {
	BlendMap  int32
	NumVars   int32
	NumVSlots int32
}

// For versions <=28
type OldFooter struct {
	LightPrecision int32
	LightError     int32
	LightLOD       int32
	Ambient        byte
	WaterColor     [3]byte
	BlendMap       byte
	LerpAngle      byte
	LerpSubDiv     byte
	LerpSubDivSize byte
	BumpError      byte
	SkyLight       [3]byte
	LavaColor      [3]byte
	WaterfallColor [3]byte
	Reserved       [10]byte
	MapTitle       [128]byte
}

type Vector struct {
	X float32
	Y float32
	Z float32
}

type Entity struct {
	Position Vector
	Attr1    int16
	Attr2    int16
	Attr3    int16
	Attr4    int16
	Attr5    int16
	Type     game.EntityType
	Reserved byte
}

type SurfaceInfo struct {
	Lmid     [2]byte
	Verts    byte
	NumVerts byte
}

func InsideWorld(size int32, vector Vector) bool {
	return vector.X >= 0 && vector.X < float32(size) && vector.Y >= 0 && vector.Y < float32(size) && vector.Z >= 0 && vector.Z < float32(size)
}

const (
	LAYER_TOP    byte = (1 << 5)
	LAYER_BOTTOM      = (1 << 6)
	LAYER_DUP         = (1 << 7)
	LAYER_BLEND       = LAYER_TOP | LAYER_BOTTOM
	MAXFACEVERTS      = 15
)

func (surface *SurfaceInfo) TotalVerts() byte {
	if (surface.NumVerts & LAYER_DUP) > 0 {
		return (surface.NumVerts & MAXFACEVERTS) * 2
	}
	return surface.NumVerts & MAXFACEVERTS
}

type SurfaceCompat struct {
	TexCoords [8]byte
	Width     byte
	Height    byte
	X         uint16
	Y         uint16
	Lmid      byte
	Layer     byte
}

type BVec struct {
	X byte
	Y byte
	Z byte
}

type NormalsCompat struct {
	Normals [4]BVec
}

type MergeCompat struct {
	U1 uint16
	U2 uint16
	V1 uint16
	V2 uint16
}

const CUBE_FACTOR = 8

const MAP_VERSION = 33

type VariableType byte

const (
	VariableTypeInt    VariableType = iota
	VariableTypeFloat               = iota
	VariableTypeString              = iota
)

const (
	ET_EMPTY        byte = iota
	ET_LIGHT             = iota
	ET_MAPMODEL          = iota
	ET_PLAYERSTART       = iota
	ET_ENVMAP            = iota
	ET_PARTICLES         = iota
	ET_SOUND             = iota
	ET_SPOTLIGHT         = iota
	ET_GAMESPECIFIC      = iota
)

const (
	OCTSAV_CHILDREN byte = iota
	OCTSAV_EMPTY         = iota
	OCTSAV_SOLID         = iota
	OCTSAV_NORMAL        = iota
	OCTSAV_LODCUB        = iota
)

const (
	VSLOT_SHPARAM  byte = iota
	VSLOT_SCALE         = iota
	VSLOT_ROTATION      = iota
	VSLOT_OFFSET        = iota
	VSLOT_SCROLL        = iota
	VSLOT_LAYER         = iota
	VSLOT_ALPHA         = iota
	VSLOT_COLOR         = iota
	VSLOT_NUM           = iota
)

type Cube struct {
	Children    *[]Cube
	SurfaceInfo [6]SurfaceInfo
	Edges       [12]byte
	Texture     [6]uint16
	Material    uint16
	Merged      byte
	Escaped     byte
}

type SlotShaderParam struct {
	Name string
	Loc  int32
	Val  [4]float32
}

type Vec2 struct {
	X float32
	Y float32
}

type IVec2 struct {
	X int32
	Y int32
}

type VSlot struct {
	Index   int32
	Changed int32
	Layer   int32
	Next    *VSlot

	Params []SlotShaderParam
	Linked bool
	Scale float32
	Rotation int32
	Offset IVec2
	Scroll Vec2
	AlphaFront float32
	AlphaBack float32
	ColorScale Vector
	GlowColor Vector
}

type IntVariable int32

func (v IntVariable) Type() VariableType {
	return VariableTypeInt
}

type FloatVariable float32

func (v FloatVariable) Type() VariableType {
	return VariableTypeFloat
}

type StringVariable string

func (v StringVariable) Type() VariableType {
	return VariableTypeString
}

type Variable interface {
	Type() VariableType
}

func GetDefaultVariables() map[string]Variable {
	return map[string]Variable{
		"ambient":           IntVariable(0x191919), // 1 -> 0xFFFFFF
		"atmo":              IntVariable(0),        // 0 -> 1
		"atmoalpha":         FloatVariable(1),      // 0 -> 1
		"atmobright":        FloatVariable(1),      // 0 -> 16
		"atmodensity":       FloatVariable(1),      // 0 -> 16
		"atmohaze":          FloatVariable(0.1),    // 0 -> 16
		"atmoheight":        FloatVariable(1),      // 1e-3f -> 1e3f
		"atmoozone":         FloatVariable(1),      // 0 -> 16
		"atmoplanetsize":    FloatVariable(1),      // 1e-3f -> 1e3f
		"atmosundisk":       IntVariable(0),        // 0 -> 0xFFFFFF
		"atmosundiskbright": FloatVariable(1),      // 0 -> 16
		"atmosundiskcorona": FloatVariable(0.4),    // 0 -> 1
		"atmosundisksize":   FloatVariable(12),     // 0 -> 90
		"atmosunlight":      IntVariable(0),        // 0 -> 0xFFFFFF
		"atmosunlightscale": FloatVariable(1),      // 0 -> 16
		"blurlms":           IntVariable(0),        // 0 -> 2
		"blurskylight":      IntVariable(0),        // 0 -> 2
		"bumperror":         IntVariable(3),        // 1 -> 16
		"causticcontrast":   FloatVariable(0.6),    // 0 -> 1
		"causticmillis":     IntVariable(75),       // 0 -> 1000
		"causticscale":      IntVariable(50),       // 0 -> 10000
		"cloudalpha":        FloatVariable(1),      // 0 -> 1
		"cloudbox":          StringVariable(""),
		"cloudboxalpha":     FloatVariable(1),      // 0 -> 1
		"cloudboxcolour":    IntVariable(0xFFFFFF), // 0 -> 0xFFFFFF
		"cloudclip":         FloatVariable(0.5),    // 0 -> 1
		"cloudcolour":       IntVariable(0xFFFFFF), // 0 -> 0xFFFFFF
		"cloudfade":         FloatVariable(0.2),    // 0 -> 1
		"cloudheight":       FloatVariable(0.2),    // -1 -> 1
		"cloudlayer":        StringVariable(""),
		"cloudoffsetx":      FloatVariable(0),      // 0 -> 1
		"cloudoffsety":      FloatVariable(0),      // 0 -> 1
		"cloudscale":        FloatVariable(1),      // 0.001 -> 64
		"cloudscrollx":      FloatVariable(0),      // -16 -> 16
		"cloudscrolly":      FloatVariable(0),      // -16 -> 16
		"cloudsubdiv":       IntVariable(16),       // 4 -> 64
		"envmapbb":          IntVariable(0),        // 0 -> 1
		"envmapradius":      IntVariable(128),      // 0 -> 10000
		"fog":               IntVariable(4000),     // 16 -> 1000024
		"fogcolour":         IntVariable(0x8099B3), // 0 -> 0xFFFFFF
		"fogdomecap":        IntVariable(1),        // 0 -> 1
		"fogdomeclip":       FloatVariable(1),      // 0 -> 1
		"fogdomeclouds":     IntVariable(1),        // 0 -> 1
		"fogdomecolour":     IntVariable(0),        // 0 -> 0xFFFFFF
		"fogdomeheight":     FloatVariable(-0.5),   // -1 -> 1
		"fogdomemax":        FloatVariable(0),      // 0 -> 1
		"fogdomemin":        FloatVariable(0),      // 0 -> 1
		"glass2colour":      IntVariable(0x2080C0), // 0 -> 0xFFFFFF
		"glass3colour":      IntVariable(0x2080C0), // 0 -> 0xFFFFFF
		"glass4colour":      IntVariable(0x2080C0), // 0 -> 0xFFFFFF
		"glasscolour":       IntVariable(0x2080C0), // 0 -> 0xFFFFFF
		"grassalpha":        FloatVariable(1),      // 0 -> 1
		"grassanimmillis":   IntVariable(3000),     // 0 -> 60000
		"grassanimscale":    FloatVariable(0.03),   // 0 -> 1
		"grasscolour":       IntVariable(0xFFFFFF), // 0 -> 0xFFFFFF
		"grassscale":        IntVariable(2),        // 1 -> 64
		"lava2colour":       IntVariable(0xFF4000), // 0 -> 0xFFFFFF
		"lava2fog":          IntVariable(50),       // 0 -> 10000
		"lava3colour":       IntVariable(0xFF4000), // 0 -> 0xFFFFFF
		"lava3fog":          IntVariable(50),       // 0 -> 10000
		"lava4colour":       IntVariable(0xFF4000), // 0 -> 0xFFFFFF
		"lava4fog":          IntVariable(50),       // 0 -> 10000
		"lavacolour":        IntVariable(0xFF4000), // 0 -> 0xFFFFFF
		"lavafog":           IntVariable(50),       // 0 -> 10000
		"lerpangle":         IntVariable(44),       // 0 -> 180
		"lerpsubdiv":        IntVariable(2),        // 0 -> 4
		"lerpsubdivsize":    IntVariable(4),        // 4 -> 128
		"lighterror":        IntVariable(8),        // 1 -> 16
		"lightlod":          IntVariable(0),        // 0 -> 10
		"lightprecision":    IntVariable(32),       // 1 -> 1024
		"maptitle":          StringVariable("Untitled Map by Unknown"),
		"mapversion":        IntVariable(MAP_VERSION), // 1 -> 0
		"minimapclip":       IntVariable(0),           // 0 -> 1
		"minimapcolour":     IntVariable(0),           // 0 -> 0xFFFFFF
		"minimapheight":     IntVariable(0),           // 0 -> 2<<16
		"refractclear":      IntVariable(0),           // 0 -> 1
		"refractsky":        IntVariable(0),           // 0 -> 1
		"shadowmapambient":  IntVariable(0),           // 0 -> 0xFFFFFF
		"shadowmapangle":    IntVariable(0),           // 0 -> 360
		"skybox":            StringVariable(""),
		"skyboxcolour":      IntVariable(0xFFFFFF), // 0 -> 0xFFFFFF
		"skylight":          IntVariable(0),        // 0 -> 0xFFFFFF
		"skytexturelight":   IntVariable(1),        // 0 -> 1
		"spincloudlayer":    FloatVariable(0),      // -720 -> 720
		"spinclouds":        FloatVariable(0),      // -720 -> 720
		"spinsky":           FloatVariable(0),      // -720 -> 720
		"sunlight":          IntVariable(0),        // 0 -> 0xFFFFFF
		"sunlightpitch":     IntVariable(90),       // -90 -> 90
		"sunlightscale":     FloatVariable(1),      // 0 -> 16
		"sunlightyaw":       IntVariable(0),        // 0 -> 360
		"water2colour":      IntVariable(0x144650), // 0 -> 0xFFFFFF
		"water2fallcolour":  IntVariable(0),        // 0 -> 0xFFFFFF
		"water2fog":         IntVariable(150),      // 0 -> 10000
		"water2spec":        IntVariable(150),      // 0 -> 1000
		"water3colour":      IntVariable(0x144650), // 0 -> 0xFFFFFF
		"water3fallcolour":  IntVariable(0),        // 0 -> 0xFFFFFF
		"water3fog":         IntVariable(150),      // 0 -> 10000
		"water3spec":        IntVariable(150),      // 0 -> 1000
		"water4colour":      IntVariable(0x144650), // 0 -> 0xFFFFFF
		"water4fallcolour":  IntVariable(0),        // 0 -> 0xFFFFFF
		"water4fog":         IntVariable(150),      // 0 -> 10000
		"water4spec":        IntVariable(150),      // 0 -> 1000
		"watercolour":       IntVariable(0x144650), // 0 -> 0xFFFFFF
		"waterfallcolour":   IntVariable(0),        // 0 -> 0xFFFFFF
		"waterfog":          IntVariable(150),      // 0 -> 10000
		"waterspec":         IntVariable(150),      // 0 -> 1000
		"yawcloudlayer":     IntVariable(0),        // 0 -> 360
		"yawclouds":         IntVariable(0),        // 0 -> 360
		"yawsky":            IntVariable(0),        // 0 -> 360
	}
}

type GameMap struct {
	Header   Header
	Entities []Entity
	Vars     map[string]Variable
	Cubes    []Cube
	VSlots   []*VSlot
}

func NewMap() *GameMap {
	return &GameMap{
		Header: Header{
			Version:    MAP_VERSION,
			GameType:   "fps",
			HeaderSize: 40,
			WorldSize:  1024,
		},
		Entities: make([]Entity, 0),
		Cubes:    make([]Cube, 8),
		Vars:     make(map[string]Variable),
		VSlots:   make([]*VSlot, 0),
	}
}
