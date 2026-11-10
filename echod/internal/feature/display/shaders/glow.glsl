// The Glow clock style's frame (glow_gpu.go): the page as the cores drew it, the colors under it moved
// on by how much they have changed since (glow-move.glsl), read between its points as glow.go's
// stretch reads the field: a screen pixel x is the field's point x / 8.
//
//   p[0]  the field's size in points, and how many pixels apart its points are
//
//   t7  the change

vec3 fx(vec2 c) {
	vec3 d = texture2D(t7, ((c - 0.5) / p[0].z + 0.5) / p[0].xy).rgb;
	return clamp(page(c) + (d - 0.5) * 0.25, 0.0, 1.0);
}
