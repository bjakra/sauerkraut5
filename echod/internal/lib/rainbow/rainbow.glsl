// The Rainbow turn screen over the page, for a GPU (gpu.go): the light round the edge and the corners
// (light.glsl, drawn at half the screen's size), the card (card.glsl, drawn when it changes) and the
// thinking dots, laid over the page at every pixel. GLSL ES 1.00, wrapped by hardware/gpu's helper,
// which gives page(c), p[] and the textures.
//
//   p[0]  whether there is light, t5's size, and the corners' rounding
//   p[1]  where the card's texture goes on the screen, and how much of it (0: no card)
//   p[2]  t6's size
//   p[3] to p[5]  the thinking dots: middle, radius, strength (0: none)
//
//   t5  the light, added, and how far in from the edge in alpha (light.glsl), a pixel to each 2 by 2
//       and one more all round
//   t6  the card, premultiplied

vec3 spot(vec2 c, vec3 v, vec4 d) {
	float a = clamp(0.5 - (length(c - d.xy) - d.z), 0.0, 1.0) * d.w;
	return mix(v, vec3(1.0), a);
}

vec3 fx(vec2 c) {
	vec3 v = page(c);
	if (p[0].x > 0.0) {
		vec4 l = texture2D(t5, (c * 0.5 + 1.0) / p[0].yz);
		v = min(v + l.rgb, 1.0) * (1.0 - p[0].w * (1.0 - clamp((l.a - 0.5) * 16.0 + 0.5, 0.0, 1.0)));
	}
	vec2 q = c - p[1].xy;
	if (q.x >= 0.0 && q.y >= 0.0 && q.x < p[1].z && q.y < p[1].w) {
		vec4 k = texture2D(t6, q / p[2].xy);
		v = k.rgb + v * (1.0 - k.a);
	}
	if (p[3].w > 0.0) {
		v = spot(c, v, p[3]);
		v = spot(c, v, p[4]);
		v = spot(c, v, p[5]);
	}
	return v;
}
