// Streaks and dots moving across the weather page (weather_gpu.go, weatherfx.go's rain, hail and
// wind): items, each a line from start to end round a point that travels along p[1] and wraps.
//
//   p[0]  the color; p[1].xy the way they travel
//   a0    where the point's path starts, where along it at the frame's start, how fast (pixels a second)
//   a1    how long the path is before it wraps, how far back it starts, the line's width, its strength
//   a2    the line's start and end from the point

varying vec2 c;
varying vec4 seg;
varying vec2 wa;

#ifdef VERTEX
void item() {
	float along = mod(a0.z + since * a0.w, a1.x) - a1.y;
	vec2 at0 = a0.xy + p[1].xy * along;
	vec2 s = at0 + a2.xy, e = at0 + a2.zw;
	float m = a1.z + 1.0;
	c = mix(min(s, e) - m, max(s, e) + m, corner);
	seg = vec4(s, e);
	wa = a1.zw;
	gl_Position = at(c);
}
#else
vec4 color() {
	vec2 d = seg.zw - seg.xy;
	float l2 = dot(d, d);
	float t = l2 > 0.0 ? clamp(dot(c - seg.xy, d) / l2, 0.0, 1.0) : 0.0;
	float a = clamp(0.5 - (length(c - (seg.xy + t * d)) - wa.x * 0.5), 0.0, 1.0) * wa.y;
	return vec4(p[0].rgb * a, a);
}
#endif
