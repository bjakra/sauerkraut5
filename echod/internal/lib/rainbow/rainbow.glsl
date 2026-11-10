// The Rainbow turn screen over the page, for a GPU (gpu.go): the light round the edge, the corners
// rounded and the card, worked out at each pixel as glow.go, geometry.go and card.go work them out on
// the cores. GLSL ES 1.00, wrapped by hardware/gpu's helper, which gives page(c), p[] and t0 to t7.
//
//   p[0]  pixels per design unit, the screen's width and height in design units
//   p[1]  the light: how far gone over to thinking, the entry's push, the voice, the gain
//   p[2]  the light's strength (0: none), half a cell in design units, the rim's strength, the
//         corners' rounding
//   p[3]  the size of t0
//   p[4]  the card: its middle and half its size, in pixels (0: no card)
//   p[5]  its rounding and its rim's width, in pixels, its strength, 1 over its scale
//   p[6]  its top and half its height, scaled, its height unscaled, whether it is at its own size
//   p[7]  where t2 starts on the screen, and t2's size in pixels of the screen
//   p[8]  words, the first layer: strength, how far moved down, size
//   p[9]  the second layer
//   p[10] which picture each layer is in: 0 t3, 1 t4
//   p[11] to p[13]  the thinking dots: middle, radius, strength (0: none)
//
//   t0  the edge: where along it (red and green), how much of the top and bottom (blue) and of the
//       right (alpha), a pixel to each 2 by 2
//   t1  the frame's light along the edge: color, and blob in eighths (red and green) below
//   t2  the card's glass, at a quarter of the screen's size
//   t3, t4  the words

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

// light is what the light adds at c, d in from the edge (glow.go's light, glowOnly and shade).
vec3 light(vec2 c, float d) {
	vec4 g = texture2D(t0, c * 0.5 / p[3].xy);
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

// words lays a layer of the card's words over v at c.
vec3 words(vec2 c, vec3 v, vec4 l, float slot) {
	if (l.x <= 0.0)
		return v;
	vec2 at = vec2((c.x - p[4].x) * p[5].w + l.z * 0.5, (c.y - p[4].y) * p[5].w + p[6].z * 0.5 - l.y) - 0.5;
	if (p[6].w > 0.5)
		at = floor(at + 0.001); // whole pixels, whatever the rounding of the card's place
	if (at.x < 0.0 || at.y < 0.0 || at.x >= l.z || at.y >= l.w)
		return v;
	vec2 t = (at + 0.5) / l.zw;
	vec4 w = (slot < 0.5 ? texture2D(t3, t) : texture2D(t4, t)) * l.x;
	return w.rgb + v * (1.0 - w.a);
}

vec3 spot(vec2 c, vec3 v, vec4 d) {
	float a = clamp(0.5 - (length(c - d.xy) - d.z), 0.0, 1.0) * d.w;
	return mix(v, vec3(1.0), a);
}

// card lays the card over v at c (card.go's paint, draw and dot).
vec3 card(vec2 c, vec3 v) {
	vec2 a = abs(c - p[4].xy) - (p[4].zw - p[5].x);
	float sd = length(max(a, 0.0)) + min(max(a.x, a.y), 0.0) - p[5].x;
	if (sd > 0.5)
		return v;
	vec3 g = texture2D(t2, (c - p[7].xy) / p[7].zw).rgb;
	g = mix(g, vec3(1.0), max(0.0, 0.10 * (1.0 - (c.y - p[6].x) / p[6].y)));
	g = words(c, g, p[8], p[10].x);
	g = words(c, g, p[9], p[10].y);
	g = mix(g, vec3(1.0), clamp(min(sd + p[5].y + 0.5, 0.5 - sd), 0.0, 1.0) * 0.35);
	float al = p[5].z * min(1.0, 0.5 - sd);
	v = g * al + v * (1.0 - al);
	if (p[11].w > 0.0) {
		v = spot(c, v, p[11]);
		v = spot(c, v, p[12]);
		v = spot(c, v, p[13]);
	}
	return v;
}

vec3 fx(vec2 c) {
	vec3 v = page(c);
	float k = p[0].x;
	float d = inside(c / k);
	if (p[2].x > 0.0 && d > -1.5 && d < maxD)
		v = min(v + light(c, max(d, 0.0)), 1.0);
	if (p[2].w > 0.0)
		v *= 1.0 - p[2].w * (1.0 - clamp(d * k + 0.5, 0.0, 1.0));
	if (p[4].z > 0.0)
		v = card(c, v);
	return v;
}
