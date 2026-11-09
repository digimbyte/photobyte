package render

import (
	"math"

	"digiphoto/internal/document"
	"digiphoto/internal/tile"
)

type rgba struct{ r, g, b, a float32 }

func Over(dst, src tile.Pixel, space document.Space) tile.Pixel {
	if src.A == 0 {
		return dst
	}
	if dst.A == 0 || src.A == 65535 {
		return src
	}
	raw := space == document.Data
	return straight(blend(premultiply(dst, raw, 1), premultiply(src, raw, 1), document.Normal), space, space)
}

var linear = func() [65536]float32 {
	var table [65536]float32
	for i := range table {
		v := float64(i) / 65535
		if v <= 0.04045 {
			table[i] = float32(v / 12.92)
		} else {
			table[i] = float32(math.Pow((v+0.055)/1.055, 2.4))
		}
	}
	return table
}()

func unit(v float32) float32 { return min(1, max(0, v)) }

func sample16(v float32) uint16 { return uint16(unit(v)*65535 + 0.5) }

func encode(v float32) uint16 {
	v = unit(v)
	if v <= 0.0031308 {
		return sample16(v * 12.92)
	}
	return sample16(float32(1.055*math.Pow(float64(v), 1.0/2.4) - 0.055))
}

func premultiply(p tile.Pixel, raw bool, opacity float32) rgba {
	a := float32(p.A) / 65535 * opacity
	if raw {
		return rgba{float32(p.R) / 65535 * a, float32(p.G) / 65535 * a, float32(p.B) / 65535 * a, a}
	}
	return rgba{linear[p.R] * a, linear[p.G] * a, linear[p.B] * a, a}
}

func straight(c rgba, from, to document.Space) tile.Pixel {
	if c.a <= 0 {
		return tile.Pixel{}
	}
	r, g, b := c.r/c.a, c.g/c.a, c.b/c.a
	switch {
	case from == document.Data:
		return tile.Pixel{R: sample16(r), G: sample16(g), B: sample16(b), A: sample16(c.a)}
	case from == document.P3 && to == document.SRGB:
		r, g, b = 1.2249402*r-0.2249402*g, -0.042057*r+1.042057*g, -0.0196376*r-0.0786361*g+1.0982737*b
	case from == document.SRGB && to == document.P3:
		r, g, b = 0.8225929*r+0.177534*g, 0.0331995*r+0.9667835*g, 0.0170854*r+0.0723957*g+0.9103015*b
	}
	return tile.Pixel{R: encode(r), G: encode(g), B: encode(b), A: sample16(c.a)}
}

func blend(dst, src rgba, mode document.Blend) rgba {
	if src.a == 0 {
		return dst
	}
	inv := 1 - src.a
	a := src.a + dst.a*inv
	if mode == document.Normal || dst.a == 0 {
		return rgba{src.r + dst.r*inv, src.g + dst.g*inv, src.b + dst.b*inv, a}
	}
	return rgba{
		blendChannel(dst.r, src.r, dst.a, src.a, mode),
		blendChannel(dst.g, src.g, dst.a, src.a, mode),
		blendChannel(dst.b, src.b, dst.a, src.a, mode),
		a,
	}
}

func blendChannel(d, s, da, sa float32, mode document.Blend) float32 {
	bg, fg := d/da, s/sa
	var value float32
	switch mode {
	case document.Multiply:
		value = bg * fg
	case document.Screen:
		value = bg + fg - bg*fg
	case document.Overlay:
		value = 2 * bg * fg
		if bg > 0.5 {
			value = 1 - 2*(1-bg)*(1-fg)
		}
	}
	return (1-sa)*d + (1-da)*s + da*sa*value
}

func adjust(c rgba, levels document.Levels, amount float32) rgba {
	if c.a == 0 || amount == 0 || levels == document.NeutralLevels() {
		return c
	}
	apply := func(v float32) float32 {
		n := unit((v/c.a - float32(levels.Black)) / float32(levels.White-levels.Black))
		if levels.Gamma != 1 {
			n = float32(math.Pow(float64(n), 1/levels.Gamma))
		}
		return v + (n*c.a-v)*amount
	}
	return rgba{apply(c.r), apply(c.g), apply(c.b), c.a}
}
