// The words of a song moving on as they are sung (lyrics_gpu.go): items, each a line of them as the
// page draws it, cut from a picture of them all (t8), gliding from where it was to where it goes and
// fading as it does, between two moments.
//
//   p[0]  t8's size
//   a0    the line's place on the screen, at rest (x0, y0, x1, y1)
//   a1    its rows in t8 (from, to), how strong it is before and after
//   a2    how far down from its place it is before and after, when it moves (from the frame's start) and
//         for how long

varying vec2 c;
varying vec4 box;
varying vec3 rows; // t8's first row of it, how strong it is

#ifdef VERTEX
void item() {
	float k = a2.w > 0.0 ? smoothstep(0.0, 1.0, clamp((since - a2.z) / a2.w, 0.0, 1.0)) : (since >= a2.z ? 1.0 : 0.0);
	float dy = mix(a2.x, a2.y, k);
	box = a0 + vec4(0.0, dy, 0.0, dy);
	rows = vec3(a1.x, mix(a1.z, a1.w, k), 0.0);
	c = mix(box.xy, box.zw, corner);
	if (rows.y <= 0.0)
		c = vec2(-10.0);
	gl_Position = at(c);
}
#else
vec4 color() {
	vec2 t = vec2(c.x - box.x, rows.x + (c.y - box.y)) / p[0].xy;
	return texture2D(t8, t) * rows.y;
}
#endif
