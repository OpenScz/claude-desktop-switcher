package aquarium

import (
	"bytes"
	"image"
	"testing"
)

func TestEveryAngleDrawsAWholeOutlinedFish(t *testing.T) {
	for _, crowned := range []bool{false, true} {
		for _, bend := range []int{-1, 0, 1} {
			for angle := range Angles {
				img := Sprite(Palettes[0], crowned, bend, angle)
				solid, edge := 0, 0
				for y := range SpriteH {
					for x := range SpriteW {
						c := img.NRGBAAt(x, y)
						if c.A == 0 {
							continue
						}
						solid++
						if c == Outline {
							edge++
						}
						if x == 0 || y == 0 || x == SpriteW-1 || y == SpriteH-1 {
							t.Fatalf("crowned=%v bend=%d angle=%d touches the sprite's edge at (%d,%d): part of the fish is cut off", crowned, bend, angle, x, y)
						}
					}
				}
				if solid < 40 || edge == 0 {
					t.Fatalf("crowned=%v bend=%d angle=%d: %d pixels, %d outline", crowned, bend, angle, solid, edge)
				}
			}
		}
	}
}

func TestTheEightAnglesDiffer(t *testing.T) {
	seen := make(map[string]int)
	for angle := range Angles {
		seen[string(Sprite(Palettes[1], true, 0, angle).Pix)] = angle
	}
	if len(seen) != Angles {
		t.Fatalf("only %d of %d angles look different", len(seen), Angles)
	}
}

func TestFacingRightAndLeftMirrorEachOther(t *testing.T) {
	right, left := Sprite(Palettes[2], false, 0, 0), Sprite(Palettes[2], false, 0, 4)
	solid := func(img *image.NRGBA) (n int) {
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i] != 0 {
				n++
			}
		}
		return n
	}
	if a, b := solid(right), solid(left); a < b*9/10 || b < a*9/10 {
		t.Fatalf("right shows %d pixels and left %d; the two sides should match", a, b)
	}
	if bytes.Equal(right.Pix, left.Pix) {
		t.Fatal("facing right and facing left drew the same picture")
	}
}

func TestSyncDropsNewFishInAndLetsClosedOnesJumpOut(t *testing.T) {
	tank := NewTank(1)
	tank.Sync([]Window{{Key: "claude", Palette: Palettes[0], Main: true, Working: 2}, {Key: "contact", Palette: Palettes[1]}})
	if len(tank.Fish) != 2 || tank.Fish[0].mode != swimming || !tank.Fish[0].Crowned {
		t.Fatalf("first sync: %+v", tank.Fish)
	}
	tank.Sync([]Window{{Key: "claude", Palette: Palettes[0], Main: true}, {Key: "claude1", Palette: Palettes[2]}})
	var contact, claude1 *Fish
	for _, f := range tank.Fish {
		switch f.Key {
		case "contact":
			contact = f
		case "claude1":
			claude1 = f
		}
	}
	if contact == nil || !contact.Leaving() || claude1 == nil || claude1.mode != falling {
		t.Fatalf("second sync: contact %+v, claude1 %+v", contact, claude1)
	}
	if tank.Fish[0].Working != 0 {
		t.Fatal("the working count did not follow the window")
	}
	for range 120 {
		tank.Step(1.0 / 12)
	}
	for _, f := range tank.Fish {
		if f.Key == "contact" {
			t.Fatal("the closed window's fish is still in the tank after ten seconds")
		}
	}
	if claude1.mode == falling {
		t.Fatal("the new fish never reached the water")
	}
}

func TestFishStayInTheWater(t *testing.T) {
	tank := NewTank(7)
	tank.Sync([]Window{{Key: "a", Palette: Palettes[0], Main: true, Working: 1}, {Key: "b", Palette: Palettes[1]}, {Key: "c", Palette: Palettes[2]}})
	for range 12 * 120 {
		tank.Step(1.0 / 12)
		for _, f := range tank.Fish {
			if f.X < 14 || f.X > 82 || f.Y < 31 || f.Y > 54 {
				t.Fatalf("fish %s left the water at (%.1f, %.1f)", f.Key, f.X, f.Y)
			}
		}
	}
}

func TestFishAtFindsTheFishUnderThePointer(t *testing.T) {
	tank := NewTank(3)
	tank.Sync([]Window{{Key: "claude", Palette: Palettes[0], Main: true}})
	f := tank.Fish[0]
	x, y := f.origin()
	if got := tank.FishAt(x+SpriteW/2, y+SpriteH/2); got != f {
		t.Fatalf("FishAt the fish's middle = %v", got)
	}
	if got := tank.FishAt(2, 2); got != nil {
		t.Fatalf("FishAt the corner = %v", got)
	}
}

func TestDrawShowsWaterAndThePlusButton(t *testing.T) {
	tank := NewTank(5)
	dst := image.NewNRGBA(image.Rect(0, 0, BowlW, BowlH))
	tank.Draw(dst)
	DrawPlus(dst, false)
	if c := dst.NRGBAAt(48, 45); c.A == 0 || c.B < c.R {
		t.Fatalf("the middle of the bowl is %+v, want water", c)
	}
	if c := dst.NRGBAAt(2, 2); c.A != 0 {
		t.Fatalf("the corner outside the bowl is %+v, want transparent", c)
	}
	center := image.Pt((PlusButton.Min.X+PlusButton.Max.X)/2, (PlusButton.Min.Y+PlusButton.Max.Y)/2)
	if c := dst.NRGBAAt(center.X, center.Y); c != rgb(255, 255, 255) {
		t.Fatalf("the plus button's middle is %+v", c)
	}
}

// The plus shows only while the pointer is on the aquarium, so it must touch
// the glass: the pointer would otherwise leave the window on its way there.
func TestThePlusButtonTouchesTheGlass(t *testing.T) {
	touching := false
	for y := PlusButton.Min.Y - 1; y <= PlusButton.Max.Y; y++ {
		for x := PlusButton.Min.X - 1; x <= PlusButton.Max.X; x++ {
			if bowlFront.NRGBAAt(x, y).A != 0 || bowlBack.NRGBAAt(x, y).A != 0 {
				touching = true
			}
		}
	}
	if !touching {
		t.Fatal("the plus button floats apart from the bowl")
	}
}
