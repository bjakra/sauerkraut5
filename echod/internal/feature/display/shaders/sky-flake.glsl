// Snowflakes drifting down the weather page (weather_gpu.go, weatherfx.go's snow and fxFlake): six
// arms, and two branches near the tip of each on the larger ones.
//
//   p[0]  the color; p[1] the page's width and left edge, the stroke's width, the radius from which a
//         flake has branches
//   a0    across, at rest; the sway's phase at the frame's start, its rate and its reach
//   a1    down, at the frame's start; how fast; how far before it wraps; the radius
//   a2    the turn at the frame's start and its rate, the strength, the page's top

varying vec2 c;
varying vec4 flake; // middle, radius, turn
varying float strength;

#ifdef VERTEX
void item() {
	float x = p[1].y + mod(a0.x + sin(a0.y + since * a0.z) * a0.w, p[1].x);
	float y = a2.w + mod(a1.x + since * a1.y, a1.z) - a1.w;
	float m = a1.w + p[1].z + 1.0;
	c = mix(vec2(x, y) - m, vec2(x, y) + m, corner);
	flake = vec4(x, y, a1.w, a2.x + since * a2.y);
	strength = a2.z;
	gl_Position = at(c);
}
#else
float segment(vec2 q, vec2 a, vec2 b) {
	vec2 d = b - a;
	return length(q - (a + clamp(dot(q - a, d) / dot(d, d), 0.0, 1.0) * d));
}

vec4 color() {
	vec2 q = c - flake.xy;
	float r = flake.z;
	float len = length(q);
	float ang = atan(q.y, q.x) - flake.w;
	ang -= floor(ang / 1.0471976 + 0.5) * 1.0471976; // folded onto the nearest arm
	vec2 v = len * vec2(cos(ang), sin(ang));
	float d = segment(v, vec2(0.0), vec2(r, 0.0));
	if (r >= p[1].w)
		d = min(d, segment(vec2(v.x, abs(v.y)), vec2(0.6 * r, 0.0), vec2(0.6 * r, 0.0) + 0.35 * r * vec2(0.7071068)));
	float a = clamp(0.5 - (d - p[1].z * 0.5), 0.0, 1.0) * strength;
	return vec4(p[0].rgb * a, a);
}
#endif
