package aquarium

import (
	"image"
	"image/color"
	"math"
)

const (
	// BowlW and BowlH are the aquarium's size in art pixels.
	BowlW = 96
	BowlH = 72

	waterLine = 21
	rimTop    = 13
)

// PlusButton is where the button listing the accounts without a window sits:
// on the rim, touching the glass, so the pointer reaches it without leaving
// the aquarium.
var PlusButton = image.Rect(66, 8, 77, 19)

var bowlBack, bowlFront = drawBowl()

// The bowl is a glass ellipse cut open at the top, with water, sand, two
// plants and a wooden stand. The glass edge and its highlight are drawn in
// front of the fish.
func drawBowl() (*image.NRGBA, *image.NRGBA) {
	back := image.NewNRGBA(image.Rect(0, 0, BowlW, BowlH))
	front := image.NewNRGBA(image.Rect(0, 0, BowlW, BowlH))
	const cx, cy, rx, ry = 48.0, 40.0, 41.0, 30.0
	inside := func(x, y int) bool {
		if y < rimTop {
			return false
		}
		dx, dy := (float64(x)+0.5-cx)/rx, (float64(y)+0.5-cy)/ry
		return dx*dx+dy*dy <= 1
	}
	for y := range BowlH {
		for x := range BowlW {
			if !inside(x, y) {
				continue
			}
			if !inside(x-1, y) || !inside(x+1, y) || !inside(x, y-1) || !inside(x, y+1) {
				front.SetNRGBA(x, y, color.NRGBA{R: 214, G: 238, B: 255, A: 230})
				continue
			}
			sand := 61 + int(math.Round(math.Sin(float64(x)/5)*1.2))
			switch {
			case y >= sand:
				c := rgb(228, 198, 142)
				if (x*13+y*7)%11 == 0 {
					c = rgb(192, 156, 100)
				}
				back.SetNRGBA(x, y, c)
			case y == waterLine:
				back.SetNRGBA(x, y, color.NRGBA{R: 176, G: 226, B: 255, A: 220})
			case y > waterLine:
				depth := float64(y-waterLine) / float64(sand-waterLine)
				dither := 0
				if (x+y)&1 == 1 {
					dither = 8
				}
				back.SetNRGBA(x, y, color.NRGBA{
					R: uint8(52 + dither),
					G: uint8(136 - int(math.Round(depth*34)) + dither),
					B: uint8(208 - int(math.Round(depth*26))),
					A: uint8(160 + int(math.Round(depth*50))),
				})
			default:
				back.SetNRGBA(x, y, color.NRGBA{R: 200, G: 232, B: 255, A: 26})
			}
		}
	}
	for y := 36; y < 61; y++ {
		sway := int(math.Round(math.Sin(float64(y)/3) * 0.8))
		back.SetNRGBA(15+sway, y, rgb(58, 150, 70))
		if y%4 == 0 {
			back.SetNRGBA(14+sway, y, rgb(92, 190, 96))
		}
		if y%4 == 2 {
			back.SetNRGBA(16+sway, y, rgb(92, 190, 96))
		}
	}
	for y := 46; y < 61; y++ {
		back.SetNRGBA(78+int(math.Round(math.Sin(float64(y)/2.5))), y, rgb(70, 168, 84))
	}
	for _, pebble := range []struct {
		x, y int
		c    color.NRGBA
	}{{30, 60, rgb(120, 120, 136)}, {31, 60, rgb(140, 140, 156)}, {62, 61, rgb(180, 120, 90)}, {63, 61, rgb(200, 140, 110)}, {50, 62, rgb(110, 110, 126)}} {
		back.SetNRGBA(pebble.x, pebble.y, pebble.c)
	}
	for degrees := 200; degrees <= 246; degrees += 2 {
		r := float64(degrees) * math.Pi / 180
		x, y := int(math.Round(cx+math.Cos(r)*rx*0.84)), int(math.Round(cy+math.Sin(r)*ry*0.84))
		if y > rimTop {
			front.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 190})
		}
	}
	for y := 68; y < 72; y++ {
		c := rgb(124, 78, 46)
		switch y {
		case 68:
			c = rgb(158, 104, 64)
		case 71:
			c = rgb(84, 52, 30)
		}
		for x := 26; x < 70; x++ {
			front.SetNRGBA(x, y, c)
		}
	}
	return back, front
}

// DrawPlus draws the button that lists the accounts without a window.
func DrawPlus(dst *image.NRGBA, hover bool) {
	fill := rgb(58, 48, 70)
	if hover {
		fill = rgb(217, 119, 87)
	}
	r := PlusButton
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := fill
			if x == r.Min.X || y == r.Min.Y || x == r.Max.X-1 || y == r.Max.Y-1 {
				c = Outline
			}
			dst.SetNRGBA(x, y, c)
		}
	}
	mx, my := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	for d := -2; d <= 2; d++ {
		dst.SetNRGBA(mx+d, my, rgb(255, 255, 255))
		dst.SetNRGBA(mx, my+d, rgb(255, 255, 255))
	}
}
