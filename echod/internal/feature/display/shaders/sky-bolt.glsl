// A storm's lightning over the weather page (weather_gpu.go, weatherfx_glass.go's lightning): the sky
// lit by each stroke of a strike, and the bolt, its glow first and its channel after.
//
//   p[3j] to p[3j+2], j up to 3  strike j: when it starts (from the frame's start) and how many strokes;
//                                when the later strokes come; how bright they are
//
//   a flash   a0: 0, the strike, its strength, how far it spreads (pixels); a1 the sky it lights; a2.x
//             the middle of the light across
//   a segment a0: 1 for its glow or 2 for its channel, the strike, its width, how bright next to the
//             main channel; a1 its ends

varying vec2 c;
varying vec4 shape; // a flash's sky, a segment's ends
varying vec4 what;  // the kind, the level, width or strength, spread or brightness
varying float mid;

const vec3 boltColor = vec3(0.980, 0.984, 1.0);
const vec3 boltGlow = vec3(0.706, 0.753, 1.0);
const vec3 flashSky = vec3(0.839, 0.863, 1.0);

#ifdef VERTEX
float level(int j) {
	vec4 s = j == 0 ? p[0] : j == 1 ? p[3] : p[6];
	vec4 at = j == 0 ? p[1] : j == 1 ? p[4] : p[7];
	vec4 peak = j == 0 ? p[2] : j == 1 ? p[5] : p[8];
	float t = since - s.x;
	if (t < 0.0 || t > 1.6)
		return 0.0;
	float l = exp(-t / 0.07);
	if (s.y > 1.0 && t >= at.x)
		l = max(l, peak.x * exp(-(t - at.x) / 0.07));
	if (s.y > 2.0 && t >= at.y)
		l = max(l, peak.y * exp(-(t - at.y) / 0.07));
	if (s.y > 3.0 && t >= at.z)
		l = max(l, peak.z * exp(-(t - at.z) / 0.07));
	return l < 0.02 ? 0.0 : l;
}

void item() {
	float lv = level(int(a0.y + 0.5));
	vec2 lo, hi;
	if (a0.x == 0.0) {
		lo = a1.xy;
		hi = a1.zw;
	} else {
		float m = a0.z * (a0.x == 1.0 ? (a0.w > 0.5 ? 3.5 : 1.4) : 0.5) + 1.5;
		lo = min(a1.xy, a1.zw) - m;
		hi = max(a1.xy, a1.zw) + m;
	}
	if (lv <= 0.0) {
		lo = vec2(-10.0);
		hi = vec2(-10.0);
	}
	c = mix(lo, hi, corner);
	shape = a1;
	what = vec4(a0.x, lv, a0.z, a0.w);
	mid = a2.x;
	gl_Position = at(c);
}
#else
float line(vec2 a, vec2 b, float w) {
	vec2 d = b - a;
	float l2 = dot(d, d);
	float t = l2 > 0.0 ? clamp(dot(c - a, d) / l2, 0.0, 1.0) : 0.0;
	return clamp(0.5 - (length(c - (a + t * d)) - w * 0.5), 0.0, 1.0);
}

vec4 color() {
	float lv = what.y;
	if (what.x == 0.0) {
		float e = (c.x - 0.5 - mid) / what.w;
		float k = lv * what.z * (1.0 - 0.65 * (c.y - 0.5 - shape.y) / (shape.w - shape.y)) * (0.35 + 0.65 * exp(-e * e));
		return vec4(flashSky * k, k);
	}
	float w = what.z, a = what.w;
	if (what.x == 1.0) {
		float g1 = a > 0.5 ? line(shape.xy, shape.zw, w * 7.0) * 0.08 * lv * a : 0.0;
		float g2 = line(shape.xy, shape.zw, w * 2.8) * 0.3 * lv * a;
		vec4 o = vec4(boltGlow * g1, g1);
		return vec4(boltGlow * g2, g2) + o * (1.0 - g2);
	}
	float k = line(shape.xy, shape.zw, max(1.0, w)) * min(1.0, 1.15 * lv) * a;
	return vec4(boltColor * k, k);
}
#endif
