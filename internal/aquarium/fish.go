// Package aquarium draws the tray's desktop aquarium: every open Claude
// window is a small voxel fish, rendered as pixel art from eight angles.
package aquarium

import (
	"image"
	"image/color"
	"math"
	"sort"
	"sync"
)

// Palette colours the fish of one account.
type Palette struct {
	Body, Back, Belly, Fin, Tag color.NRGBA
}

func rgb(r, g, b uint8) color.NRGBA { return color.NRGBA{R: r, G: g, B: b, A: 255} }

// Palettes go to the saved accounts in order.
var Palettes = []Palette{
	{Body: rgb(217, 119, 87), Back: rgb(176, 86, 58), Belly: rgb(246, 196, 172), Fin: rgb(236, 152, 112), Tag: rgb(232, 149, 111)},
	{Body: rgb(56, 166, 158), Back: rgb(36, 118, 112), Belly: rgb(172, 228, 220), Fin: rgb(96, 198, 190), Tag: rgb(111, 208, 198)},
	{Body: rgb(140, 106, 200), Back: rgb(102, 74, 156), Belly: rgb(208, 188, 242), Fin: rgb(170, 140, 222), Tag: rgb(183, 156, 230)},
	{Body: rgb(222, 178, 64), Back: rgb(176, 134, 36), Belly: rgb(248, 226, 160), Fin: rgb(240, 200, 96), Tag: rgb(240, 204, 110)},
	{Body: rgb(222, 104, 150), Back: rgb(170, 70, 110), Belly: rgb(248, 190, 212), Fin: rgb(236, 140, 180), Tag: rgb(238, 150, 186)},
	{Body: rgb(104, 176, 84), Back: rgb(68, 128, 56), Belly: rgb(196, 232, 170), Fin: rgb(140, 204, 112), Tag: rgb(150, 210, 120)},
}

// Unknown colours the main window's fish when its account has no saved profile.
var Unknown = Palette{Body: rgb(150, 150, 160), Back: rgb(110, 110, 120), Belly: rgb(210, 210, 218), Fin: rgb(175, 175, 185), Tag: rgb(190, 190, 200)}

// Outline is the dark edge around every sprite.
var Outline = rgb(20, 15, 28)

var (
	eyeWhite = rgb(250, 250, 244)
	eyeBlack = rgb(26, 20, 34)
	gold     = rgb(246, 198, 66)
	gem      = rgb(222, 58, 72)
)

const (
	// SpriteW and SpriteH bound every fish sprite, crown and tail included.
	SpriteW = 38
	SpriteH = 30
	// Angles are 45° apart: 0 faces right, 2 faces the viewer, 4 faces left.
	Angles = 8

	voxelSize   = 2.0
	cameraPitch = 22.0
	modelCenter = -1.5
)

type voxel struct {
	x, y, z int
	c       color.NRGBA
}

// The fish faces +x: a body ellipsoid, a forked tail bent sideways by bend,
// fins, eyes on both sides and, for the main window, a crown.
func fishVoxels(p Palette, crowned bool, bend int) []voxel {
	cells := make(map[[3]int]color.NRGBA)
	put := func(x, y, z int, c color.NRGBA) { cells[[3]int{x, y, z}] = c }
	for x := -5; x <= 5; x++ {
		for y := -3; y <= 3; y++ {
			for z := -2; z <= 2; z++ {
				fx, fy, fz := float64(x)/4.6, float64(y)/2.6, float64(z)/2.3
				if fx*fx+fy*fy+fz*fz > 1 {
					continue
				}
				switch {
				case y >= 2:
					put(x, y, z, p.Back)
				case y <= -2:
					put(x, y, z, p.Belly)
				default:
					put(x, y, z, p.Body)
				}
			}
		}
	}
	for x := -8; x <= -5; x++ {
		span := -5 - x
		z := int(math.Round(float64(bend*span) / 3))
		for y := -span; y <= span; y++ {
			if x != -8 || y != 0 {
				put(x, y, z, p.Fin)
			}
		}
	}
	for x := -2; x <= 0; x++ {
		put(x, 3, 0, p.Fin)
	}
	put(-1, 4, 0, p.Fin)
	for _, z := range []int{-3, 3} {
		put(1, -2, z, p.Fin)
		put(0, -2, z, p.Fin)
	}
	for _, z := range []int{-1, 1} {
		put(2, 1, z, eyeWhite)
		put(3, 1, z, eyeBlack)
	}
	if crowned {
		for x := 1; x <= 3; x++ {
			for z := -1; z <= 1; z++ {
				put(x, 3, z, gold)
			}
		}
		for _, corner := range [][2]int{{1, -1}, {3, -1}, {1, 1}, {3, 1}} {
			put(corner[0], 4, corner[1], gold)
		}
		put(2, 4, 0, gem)
	}
	voxels := make([]voxel, 0, len(cells))
	for at, c := range cells {
		voxels = append(voxels, voxel{x: at[0], y: at[1], z: at[2], c: c})
	}
	sort.Slice(voxels, func(i, j int) bool {
		a, b := voxels[i], voxels[j]
		if a.x != b.x {
			return a.x < b.x
		}
		if a.y != b.y {
			return a.y < b.y
		}
		return a.z < b.z
	})
	return voxels
}

type spriteKey struct {
	palette Palette
	crowned bool
	bend    int
	angle   int
}

var sprites = struct {
	sync.Mutex
	cache map[spriteKey]*image.NRGBA
}{cache: make(map[spriteKey]*image.NRGBA)}

// Sprite is an account's fish seen from angle (see Angles), its tail bent by
// bend (-1, 0 or 1). Sprites are drawn once and kept.
func Sprite(p Palette, crowned bool, bend, angle int) *image.NRGBA {
	key := spriteKey{palette: p, crowned: crowned, bend: bend, angle: ((angle % Angles) + Angles) % Angles}
	sprites.Lock()
	defer sprites.Unlock()
	if img, ok := sprites.cache[key]; ok {
		return img
	}
	img := render(fishVoxels(p, crowned, bend), float64(key.angle)*360/Angles)
	sprites.cache[key] = img
	return img
}

var faceNormals = [6][3]int{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}}

type face struct {
	points [4][2]float64
	depth  float64
	color  color.NRGBA
}

// render turns the model by yaw, tilts the camera down a little and paints
// the visible cube faces back to front, lit from the upper left.
func render(voxels []voxel, yaw float64) *image.NRGBA {
	t, p := yaw*math.Pi/180, cameraPitch*math.Pi/180
	rotate := func(x, y, z float64) (float64, float64, float64) {
		turnedX := x*math.Cos(t) - z*math.Sin(t)
		turnedZ := x*math.Sin(t) + z*math.Cos(t)
		return turnedX, y*math.Cos(p) - turnedZ*math.Sin(p), y*math.Sin(p) + turnedZ*math.Cos(p)
	}
	lx, ly, lz := -0.45, 0.75, 0.5
	length := math.Sqrt(lx*lx + ly*ly + lz*lz)
	lx, ly, lz = lx/length, ly/length, lz/length
	filled := make(map[[3]int]bool, len(voxels))
	for _, v := range voxels {
		filled[[3]int{v.x, v.y, v.z}] = true
	}
	cx, cy := SpriteW/2.0, SpriteH/2.0+1
	var faces []face
	for _, v := range voxels {
		for _, n := range faceNormals {
			if filled[[3]int{v.x + n[0], v.y + n[1], v.z + n[2]}] {
				continue
			}
			nx, ny, nz := rotate(float64(n[0]), float64(n[1]), float64(n[2]))
			if nz <= 0.02 {
				continue
			}
			f := face{color: shade(v.c, 0.66+0.46*math.Max(0, nx*lx+ny*ly+nz*lz))}
			for i, corner := range faceCorners(v, n) {
				x, y, z := rotate(corner[0]-modelCenter, corner[1], corner[2])
				f.points[i] = [2]float64{cx + x*voxelSize, cy - y*voxelSize}
				f.depth += z / 4
			}
			faces = append(faces, f)
		}
	}
	sort.SliceStable(faces, func(i, j int) bool { return faces[i].depth < faces[j].depth })
	img := image.NewNRGBA(image.Rect(0, 0, SpriteW, SpriteH))
	for _, f := range faces {
		fillQuad(img, f.points, f.color)
	}
	outline(img)
	return img
}

func faceCorners(v voxel, n [3]int) [4][3]float64 {
	center := [3]float64{float64(v.x) + float64(n[0])/2, float64(v.y) + float64(n[1])/2, float64(v.z) + float64(n[2])/2}
	a, b := [3]float64{1, 0, 0}, [3]float64{0, 0, 1}
	if n[0] != 0 {
		a = [3]float64{0, 1, 0}
	}
	if n[2] != 0 {
		b = [3]float64{0, 1, 0}
	}
	var corners [4][3]float64
	for i, s := range [4][2]float64{{1, 1}, {1, -1}, {-1, -1}, {-1, 1}} {
		for k := range 3 {
			corners[i][k] = center[k] + (a[k]*s[0]+b[k]*s[1])/2
		}
	}
	return corners
}

func shade(c color.NRGBA, light float64) color.NRGBA {
	scale := func(v uint8) uint8 { return uint8(math.Min(255, math.Round(float64(v)*light))) }
	return color.NRGBA{R: scale(c.R), G: scale(c.G), B: scale(c.B), A: 255}
}

// fillQuad fills the pixels whose centres lie inside the convex quad or within
// 0.3 px of its edge, which closes the seams between neighbouring faces.
func fillQuad(img *image.NRGBA, points [4][2]float64, c color.NRGBA) {
	area := 0.0
	minX, minY, maxX, maxY := points[0][0], points[0][1], points[0][0], points[0][1]
	for i := range 4 {
		j := (i + 1) % 4
		area += points[i][0]*points[j][1] - points[j][0]*points[i][1]
		minX, maxX = math.Min(minX, points[i][0]), math.Max(maxX, points[i][0])
		minY, maxY = math.Min(minY, points[i][1]), math.Max(maxY, points[i][1])
	}
	if math.Abs(area) < 1e-9 {
		return
	}
	sign := 1.0
	if area < 0 {
		sign = -1
	}
	for y := int(math.Floor(minY - 1)); y <= int(math.Ceil(maxY+1)); y++ {
		for x := int(math.Floor(minX - 1)); x <= int(math.Ceil(maxX+1)); x++ {
			if !image.Pt(x, y).In(img.Rect) {
				continue
			}
			px, py := float64(x)+0.5, float64(y)+0.5
			inside := true
			for i := range 4 {
				j := (i + 1) % 4
				ex, ey := points[j][0]-points[i][0], points[j][1]-points[i][1]
				edge := math.Hypot(ex, ey)
				if edge == 0 {
					continue
				}
				if sign*(ex*(py-points[i][1])-ey*(px-points[i][0]))/edge < -0.3 {
					inside = false
					break
				}
			}
			if inside {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func outline(img *image.NRGBA) {
	solid := func(x, y int) bool { return image.Pt(x, y).In(img.Rect) && img.NRGBAAt(x, y).A != 0 }
	var edge []image.Point
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			if !solid(x, y) && (solid(x+1, y) || solid(x-1, y) || solid(x, y+1) || solid(x, y-1)) {
				edge = append(edge, image.Pt(x, y))
			}
		}
	}
	for _, p := range edge {
		img.SetNRGBA(p.X, p.Y, Outline)
	}
}

// Blend paints c over the pixel at (x, y), both with straight alpha.
func Blend(dst *image.NRGBA, x, y int, c color.NRGBA) {
	if c.A == 0 || !image.Pt(x, y).In(dst.Rect) {
		return
	}
	i := dst.PixOffset(x, y)
	if c.A == 255 {
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c.R, c.G, c.B, 255
		return
	}
	sa, da := float64(c.A)/255, float64(dst.Pix[i+3])/255
	out := sa + da*(1-sa)
	for k, v := range [3]uint8{c.R, c.G, c.B} {
		dst.Pix[i+k] = uint8((float64(v)*sa+float64(dst.Pix[i+k])*da*(1-sa))/out + 0.5)
	}
	dst.Pix[i+3] = uint8(out*255 + 0.5)
}

// DrawImage paints src over dst with its top left corner at (x, y), each
// source pixel as a square of scale pixels.
func DrawImage(dst, src *image.NRGBA, x, y, scale int) {
	for sy := src.Rect.Min.Y; sy < src.Rect.Max.Y; sy++ {
		for sx := src.Rect.Min.X; sx < src.Rect.Max.X; sx++ {
			c := src.NRGBAAt(sx, sy)
			if c.A == 0 {
				continue
			}
			for dy := range scale {
				for dx := range scale {
					Blend(dst, x+(sx-src.Rect.Min.X)*scale+dx, y+(sy-src.Rect.Min.Y)*scale+dy, c)
				}
			}
		}
	}
}
