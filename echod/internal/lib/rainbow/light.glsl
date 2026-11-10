// The light round the edge and the corners' rounding, for a GPU (gpu.go): worked out at half the
// screen's size, as glow.go does near the edge, into a texture the frame's program (rainbow.glsl) adds
// over the page. A pass's program (hardware/gpu's effects): h is in the texture's pixels, each the
// screen's 2 by 2, and one more all round.
//
//   p[0]  pixels per design unit, the screen's width and height in design units
//   p[1]  the light: how far gone over to thinking, the entry's push, the voice, the gain
//   p[2]  the light's strength, half a cell in design units, the rim's strength
//   p[3]  the half grid's size (t0's; the texture's is two more each way)
//
//   t0  the edge: where along it (red and green), how much of the top and bottom (blue) and of the
//       right (alpha)
//   t1  the frame's light along the edge: color, and blob in eighths (red and green) below
//
// Out: the light, to be added, and in alpha how far in from the edge it is, for the corners' rounding.

const float maxD = 110.0;
const float fade0 = 55.0;
const float farD = 24.0;
const float cornerR = 38.0;

// inside is how far q (in design units) is inside the screen's rounded edge (geometry.go's sdf).
float inside(vec2 q) {
	vec2 hs = p[0].yz * 0.5;
	vec2 a = abs(q - hs);
	vec2 e = a - (hs - cornerR);
	if (e.x > 0.0 && e.y > 0.0)
		return cornerR - length(e);
	return min(hs.x - a.x, hs.y - a.y);
}

// cdf is the normal distribution, closely enough (glow.go's table).
float cdf(float z) {
	return 1.0 / (1.0 + exp(-1.5976 * z * (1.0 + 0.044715 * z * z)));
}

float byte2(vec2 v) {
	return floor(v.x * 255.0 + 0.5) * 256.0 + floor(v.y * 255.0 + 0.5);
}

// light is what the light adds at h, a pixel of the half grid, d in from the edge (glow.go's light,
// glowOnly and shade).
vec3 light(vec2 h, float d) {
	vec4 g = texture2D(t0, h / p[3].xy);
	float u = byte2(g.rg) / 65536.0;
	vec3 col = texture2D(t1, vec2(u, 0.25)).rgb;
	float blob = byte2(texture2D(t1, vec2(u, 0.75)).rg) * (8.0 / 65535.0);
	float ny = g.b, nx = g.a;
	float tm = p[1].x, boost = p[1].y, amp = p[1].z;
	float wEn = 0.5 + 0.5 * ny, wEt = 0.05 + 0.95 * ny * ny;
	float wE = wEn + (wEt - wEn) * tm + boost * 0.75 * nx;
	float reach = 10.0 + amp * 24.0 * (0.4 + 0.6 * ny);
	reach += (12.0 + 34.0 * ny - reach) * tm + boost * (6.0 + 16.0 * nx);
	float fall = exp(-d / reach);
	if (d < farD)
		fall += 0.85 * exp(-d / 2.6);
	float i = wE * blob * p[1].w * fall * (1.0 - smoothstep(fade0, maxD, d));
	vec3 v = col * i + max(0.0, i - 1.0) * (120.0 / 256.0);
	if (d >= farD)
		return min(v, 1.0);
	// Near the edge: the line along it, 2w wide and centered on it, with its glow, and the rim.
	float vis = p[2].x, cell = p[2].y;
	float w = (1.4 + (2.0 + 4.5 * amp) * wE * min(1.4, blob)) * max(vis, 0.6);
	float cover = clamp((w - d) / cell + 0.5, 0.0, 1.0);
	float line = vis * (cover + 0.7 * (cdf((w - d) / 4.0) - cdf((-w - d) / 4.0)));
	float white = clamp(blob - 0.8, 0.0, 1.0) * 0.35;
	v = min(v + p[2].z * clamp((1.0 - d) / cell + 0.5, 0.0, 1.0), 1.0);
	return min(v + mix(col, vec3(1.0), white) * line, 1.0);
}

vec4 pass(vec2 h) {
	// The texture has a pixel more than the half grid all round, so that the screen's outermost pixels
	// are read between two of its pixels too: h less one is the half grid's.
	h -= 1.0;
	vec2 c = h * 2.0; // the middle of the screen's 2 by 2 pixels this one stands for
	float k = p[0].x;
	float d = inside(c / k);
	vec3 v = vec3(0.0);
	if (d > -1.5 && d < maxD)
		v = light(h, max(d, 0.0));
	// How far in from the edge, in pixels: within 8 of it, where the corners are rounded, it is read
	// smoothly between these pixels, and is as it is at each of the screen's.
	return vec4(v, clamp(d * k / 16.0 + 0.5, 0.0, 1.0));
}
