// The card, for a GPU (gpu.go): its picture as card.go's paint lays it over the page, premultiplied, into
// a texture of its own, drawn again only when the card changes. A pass's program (hardware/gpu's
// effects): c is in the texture's pixels, p[0] where its first pixel is on the screen.
//
//   p[0]  where the texture's first pixel is on the screen, in pixels
//   p[1]  the card's middle and half its size, in pixels
//   p[2]  its rounding and its rim's width, in pixels, its strength, 1 over its scale
//   p[3]  its top and half its height, scaled, its height unscaled, whether it is at its own size
//   p[4]  where t2 starts on the screen, and t2's size in pixels of the screen
//   p[5]  words, the first layer: strength, how far moved down, size
//   p[6]  the second layer
//   p[7]  which picture each layer is in: 0 t3, 1 t4
//
//   t2  the glass, at a quarter of the screen's size
//   t3, t4  the words

// words lays a layer of the card's words over v at c.
vec3 words(vec2 c, vec3 v, vec4 l, float slot) {
	if (l.x <= 0.0)
		return v;
	vec2 at = vec2((c.x - p[1].x) * p[2].w + l.z * 0.5, (c.y - p[1].y) * p[2].w + p[3].z * 0.5 - l.y) - 0.5;
	if (p[3].w > 0.5)
		at = floor(at + 0.001); // whole pixels, whatever the rounding of the card's place
	if (at.x < 0.0 || at.y < 0.0 || at.x >= l.z || at.y >= l.w)
		return v;
	vec2 t = (at + 0.5) / l.zw;
	vec4 w = (slot < 0.5 ? texture2D(t3, t) : texture2D(t4, t)) * l.x;
	return w.rgb + v * (1.0 - w.a);
}

vec4 pass(vec2 at) {
	vec2 c = at + p[0].xy;
	vec2 a = abs(c - p[1].xy) - (p[1].zw - p[2].x);
	float sd = length(max(a, 0.0)) + min(max(a.x, a.y), 0.0) - p[2].x;
	if (sd > 0.5)
		return vec4(0.0);
	vec3 g = texture2D(t2, (c - p[4].xy) / p[4].zw).rgb;
	g = mix(g, vec3(1.0), max(0.0, 0.10 * (1.0 - (c.y - p[3].x) / p[3].y)));
	g = words(c, g, p[5], p[7].x);
	g = words(c, g, p[6], p[7].y);
	g = mix(g, vec3(1.0), clamp(min(sd + p[2].y + 0.5, 0.5 - sd), 0.0, 1.0) * 0.35);
	float al = p[2].z * min(1.0, 0.5 - sd);
	return vec4(g * al, al);
}
