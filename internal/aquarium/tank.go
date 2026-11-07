package aquarium

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
)

// Window is one open Claude window as the aquarium shows it.
type Window struct {
	Key     string
	Palette Palette
	Main    bool
	Working int
}

// Fish is the fish of one open window. It swims, turns through the eight
// angles, looks at the viewer now and then and blows bubbles while a session
// works in its window.
type Fish struct {
	Key     string
	Palette Palette
	Crowned bool
	Working int
	X, Y    float64

	heading, target float64
	mode            fishMode
	wag, rest       float64
	bubble          float64
	vx, vy, hop     float64
	splashed        bool
	leaving, gone   bool
}

type fishMode int

const (
	swimming fishMode = iota
	looking
	rising
	jumping
	falling
)

type particle struct{ x, y, vx, vy, life float64 }

// Tank holds the fish and moves them along.
type Tank struct {
	Fish    []*Fish
	bubbles []particle
	splash  []particle
	rnd     *rand.Rand
	clock   float64
	synced  bool
}

func NewTank(seed uint64) *Tank {
	return &Tank{rnd: rand.New(rand.NewPCG(seed, 1))}
}

// Sync makes the fish match the open windows. The first time the fish are
// already swimming; after that a new window drops a fish in and a closed one
// makes its fish jump out.
func (t *Tank) Sync(windows []Window) {
	open := make(map[string]Window, len(windows))
	for _, w := range windows {
		open[w.Key] = w
	}
	for _, f := range t.Fish {
		if f.leaving {
			continue
		}
		w, ok := open[f.Key]
		if !ok {
			f.leaving, f.mode, f.target = true, rising, 90
			continue
		}
		f.Palette, f.Crowned, f.Working = w.Palette, w.Main, w.Working
		delete(open, f.Key)
	}
	for i, w := range windows {
		if _, missing := open[w.Key]; !missing {
			continue
		}
		f := &Fish{Key: w.Key, Palette: w.Palette, Crowned: w.Main, Working: w.Working, rest: 2 + 4*t.rnd.Float64(), bubble: t.rnd.Float64()}
		if t.synced {
			f.X, f.Y, f.heading, f.target, f.mode, f.vy = 48, -10, 90, 90, falling, 10
		} else {
			f.X, f.Y = 30+float64(i*23%40), 36+float64(i*9%16)
			if i%2 == 1 {
				f.heading = 180
			}
			f.target, f.mode = f.heading, swimming
		}
		t.Fish = append(t.Fish, f)
	}
	t.synced = true
}

// Step moves everything dt seconds on.
func (t *Tank) Step(dt float64) {
	t.clock += dt
	for _, f := range t.Fish {
		switch f.mode {
		case swimming, looking:
			f.rest -= dt
			if f.mode == swimming {
				f.X += math.Cos(f.heading*math.Pi/180) * 9 * dt
				if f.X > 72 {
					f.target = 180
				}
				if f.X < 24 {
					f.target = 0
				}
				if f.rest <= 0 {
					f.mode, f.target, f.rest = looking, 90, 1.4
				}
			} else if f.rest <= 0 {
				f.mode, f.target, f.rest = swimming, 180, 3+4*t.rnd.Float64()
				if f.X < 48 {
					f.target = 0
				}
			}
			f.Y = math.Max(31, math.Min(54, f.Y+math.Sin(t.clock/0.7+f.X)*0.08))
			if f.Working > 0 {
				if f.bubble -= dt; f.bubble <= 0 {
					f.bubble = 0.7
					h := f.heading * math.Pi / 180
					t.bubbles = append(t.bubbles, particle{x: f.X + math.Cos(h)*8, y: f.Y - 1})
				}
			}
		case rising:
			f.Y -= 22 * dt
			if f.Y <= waterLine+3 {
				f.mode, f.vy, f.vx = jumping, -62, 40
				if f.X < BowlW/2 {
					f.vx = -40
				}
				t.splashAt(f.X)
			}
		case jumping:
			f.vy += 110 * dt
			f.X += f.vx * dt
			f.Y += f.vy * dt
			if f.X < -SpriteW || f.X > BowlW+SpriteW || f.Y > BowlH+SpriteH {
				f.gone = true
			}
		case falling:
			f.vy += 110 * dt
			f.Y += f.vy * dt
			if f.Y >= waterLine+2 && !f.splashed {
				f.splashed = true
				f.vy *= 0.25
				t.splashAt(f.X)
			}
			if f.splashed && f.Y >= 38 {
				f.mode, f.rest = looking, 1.2
			}
		}
		diff := f.target - f.heading
		f.heading += math.Copysign(math.Min(math.Abs(diff), 150*dt), diff)
		if f.mode == swimming {
			f.wag += dt * 9
		} else {
			f.wag += dt * 4
		}
		if f.hop > 0 {
			f.hop -= dt * 1.6
		}
	}
	kept := t.Fish[:0]
	for _, f := range t.Fish {
		if !f.gone {
			kept = append(kept, f)
		}
	}
	t.Fish = kept
	bubbles := t.bubbles[:0]
	for _, b := range t.bubbles {
		b.y -= 10 * dt
		b.x += math.Sin(b.y) * 0.15
		if b.y > waterLine+1 {
			bubbles = append(bubbles, b)
		}
	}
	t.bubbles = bubbles
	splash := t.splash[:0]
	for _, s := range t.splash {
		s.vy += 90 * dt
		s.x += s.vx * dt
		s.y += s.vy * dt
		if s.life -= dt; s.life > 0 {
			splash = append(splash, s)
		}
	}
	t.splash = splash
}

func (t *Tank) splashAt(x float64) {
	for range 10 {
		t.splash = append(t.splash, particle{x: x, y: waterLine, vx: (t.rnd.Float64() - 0.5) * 40, vy: -20 - t.rnd.Float64()*30, life: 0.6})
	}
}

// Hop makes the fish jump a little and turn to the viewer, as when its window
// comes to the front.
func (f *Fish) Hop() {
	if f.mode == swimming || f.mode == looking {
		f.hop, f.mode, f.target, f.rest = 1, looking, 90, 1.6
	}
}

// Leaving reports whether the fish is on its way out.
func (f *Fish) Leaving() bool { return f.leaving }

func (f *Fish) angle() int {
	return ((int(math.Round(f.heading/45)) % Angles) + Angles) % Angles
}

func (f *Fish) sprite() *image.NRGBA {
	return Sprite(f.Palette, f.Crowned, []int{0, 1, 0, -1}[int(f.wag)%4], f.angle())
}

func (f *Fish) origin() (int, int) {
	lift := 0
	if f.hop > 0 {
		lift = int(math.Round(math.Sin(f.hop*math.Pi) * 5))
	}
	return int(math.Round(f.X - SpriteW/2)), int(math.Round(f.Y-SpriteH/2)) - lift
}

// TagPoint is where the account's name sits, above the fish, in art pixels.
func (f *Fish) TagPoint() (int, int) {
	return int(math.Round(f.X)), int(math.Round(f.Y)) - 10
}

// Draw paints the bowl, the fish, bubbles and splashes into dst, BowlW×BowlH.
func (t *Tank) Draw(dst *image.NRGBA) {
	copy(dst.Pix, bowlBack.Pix)
	for _, f := range t.Fish {
		x, y := f.origin()
		DrawImage(dst, f.sprite(), x, y, 1)
	}
	for _, b := range t.bubbles {
		x, y := int(math.Round(b.x)), int(math.Round(b.y))
		Blend(dst, x, y, color.NRGBA{R: 235, G: 248, B: 255, A: 230})
		Blend(dst, x+1, y-2, color.NRGBA{R: 235, G: 248, B: 255, A: 230})
	}
	for _, s := range t.splash {
		Blend(dst, int(math.Round(s.x)), int(math.Round(s.y)), color.NRGBA{R: 190, G: 230, B: 255, A: 242})
	}
	DrawImage(dst, bowlFront, 0, 0, 1)
}

// FishAt finds the fish drawn at the art pixel (x, y); a fish on its way out
// does not count.
func (t *Tank) FishAt(x, y int) *Fish {
	for i := len(t.Fish) - 1; i >= 0; i-- {
		f := t.Fish[i]
		if f.leaving {
			continue
		}
		ox, oy := f.origin()
		if p := image.Pt(x-ox, y-oy); p.In(image.Rect(0, 0, SpriteW, SpriteH)) && f.sprite().NRGBAAt(p.X, p.Y).A != 0 {
			return f
		}
	}
	return nil
}
